package hosting

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The bounds of an image tar a project's build left (plan M19 D30): what the server reads of it to
// clean it, and what it lets Docker load.
const (
	maxEntries  = 10000
	maxLayers   = 127
	maxMetaFile = 1 << 20 // manifest.json, index.json, oci-layout, an image's config
)

// Image is what the cleaner read of an image tar: the ids Docker gives it, its config's digest in the
// overlay2 store and its manifest's in containerd's (plan M19 D42), and what its config asks of the
// machine.
type Image struct {
	ID       string
	Manifest string   // the manifest index.json names, checked; "" without index.json
	Volumes  []string // the image's VOLUMEs
	Ports    []string // its EXPOSEd ports, as 8080/tcp
	// Labels are its LABELs named crew.…, which Docker would give the container beside Crew's own.
	Labels []string
}

var blobName = regexp.MustCompile(`^blobs/sha256/[0-9a-f]{64}$`)

// knownEntry says whether name is one an image tar of `docker save` (Docker 25 and later, the OCI
// layout with manifest.json beside it) holds: no other is loaded.
func knownEntry(name string, dir bool) bool {
	if dir {
		return name == "blobs/" || name == "blobs/sha256/"
	}
	switch name {
	case "manifest.json", "index.json", "oci-layout", "repositories":
		return true
	}
	return blobName.MatchString(name)
}

// CleanImage writes an image tar from src to w so that it names no image (plan M19 D1): manifest.json
// without RepoTags, no repositories, and index.json without the names containerd and OCI give a
// manifest. Every other entry is copied as it is. A tar that is not one image as `docker save` writes
// it is refused: an entry that is no plain file or folder of the layout, a name twice, more than
// maxEntries entries or maxLayers layers, a config whose digest is not its name. src is read twice,
// once to learn the layout and once to write it.
func CleanImage(src io.ReadSeeker, w io.Writer) (Image, error) {
	meta, err := readLayout(src)
	if err != nil {
		return Image{}, err
	}
	var m []map[string]json.RawMessage
	if err := json.Unmarshal(meta["manifest.json"], &m); err != nil || len(m) != 1 {
		return Image{}, errors.New("the image tar's manifest.json is not one image")
	}
	var config string
	var layers []string
	if json.Unmarshal(m[0]["Config"], &config) != nil || !blobName.MatchString(config) {
		return Image{}, errors.New("the image tar's manifest.json names no config of the layout")
	}
	if json.Unmarshal(m[0]["Layers"], &layers) != nil || len(layers) > maxLayers {
		return Image{}, fmt.Errorf("the image has more than %d layers, or none the manifest can name", maxLayers)
	}
	for _, l := range layers {
		if !blobName.MatchString(l) {
			return Image{}, fmt.Errorf("the image's layer %q is not in the layout", l)
		}
	}
	m[0]["RepoTags"] = json.RawMessage("null")
	manifest, err := json.Marshal(m)
	if err != nil {
		return Image{}, err
	}
	var index []byte
	manifestBlob := ""
	if raw, ok := meta["index.json"]; ok {
		var digest string
		if index, digest, err = cleanIndex(raw); err != nil {
			return Image{}, err
		}
		manifestBlob = "blobs/sha256/" + strings.TrimPrefix(digest, "sha256:")
		if !strings.HasPrefix(digest, "sha256:") || !blobName.MatchString(manifestBlob) {
			return Image{}, errors.New("the image tar's index.json names no manifest of the layout")
		}
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return Image{}, err
	}
	tr, tw := tar.NewReader(src), tar.NewWriter(w)
	var img Image
	manifestSeen := ""
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Image{}, fmt.Errorf("the image tar: %w", err)
		}
		// The file is the server's, but its second reading is checked as its first.
		if (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir) || !knownEntry(h.Name, h.Typeflag == tar.TypeDir) {
			return Image{}, fmt.Errorf("the image tar's %q changed while it was read", h.Name)
		}
		var body io.Reader = tr
		switch h.Name {
		case "repositories":
			continue
		case "manifest.json":
			body, h.Size = bytes.NewReader(manifest), int64(len(manifest))
		case "index.json":
			body, h.Size = bytes.NewReader(index), int64(len(index))
		case config:
			raw, err := io.ReadAll(io.LimitReader(tr, maxMetaFile+1))
			if err != nil || len(raw) > maxMetaFile {
				return Image{}, errors.New("the image's config is larger than the cleaner reads")
			}
			sum := sha256.Sum256(raw)
			if "blobs/sha256/"+hex.EncodeToString(sum[:]) != config {
				return Image{}, errors.New("the image's config is not the one its name says")
			}
			if img, err = readConfig(raw); err != nil {
				return Image{}, err
			}
			img.ID = "sha256:" + hex.EncodeToString(sum[:])
			body = bytes.NewReader(raw)
		case manifestBlob:
			raw, err := io.ReadAll(io.LimitReader(tr, maxMetaFile+1))
			if err != nil || len(raw) > maxMetaFile {
				return Image{}, errors.New("the image's manifest is larger than the cleaner reads")
			}
			sum := sha256.Sum256(raw)
			var man struct {
				Config struct {
					Digest string `json:"digest"`
				} `json:"config"`
			}
			if "blobs/sha256/"+hex.EncodeToString(sum[:]) != manifestBlob || json.Unmarshal(raw, &man) != nil ||
				"blobs/sha256/"+strings.TrimPrefix(man.Config.Digest, "sha256:") != config {
				return Image{}, errors.New("the image's manifest is not the one index.json names, or names another config")
			}
			manifestSeen = "sha256:" + hex.EncodeToString(sum[:])
			body = bytes.NewReader(raw)
		}
		if err := tw.WriteHeader(h); err != nil {
			return Image{}, err
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := io.Copy(tw, body); err != nil {
				return Image{}, err
			}
		}
	}
	if img.ID == "" {
		return Image{}, errors.New("the image tar has no config")
	}
	if manifestBlob != "" && manifestSeen == "" {
		return Image{}, errors.New("the image tar has no manifest where index.json says")
	}
	img.Manifest = manifestSeen
	return img, tw.Close()
}

// readLayout reads the tar's headers once: each entry a plain file or folder of the layout, each name
// once, within the bounds; it keeps the small files it rewrites.
func readLayout(src io.Reader) (map[string][]byte, error) {
	tr := tar.NewReader(src)
	seen := map[string]bool{}
	meta := map[string][]byte{}
	for n := 0; ; n++ {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("the image tar: %w", err)
		}
		if n >= maxEntries {
			return nil, fmt.Errorf("the image tar has more than %d entries", maxEntries)
		}
		name := h.Name
		dir := h.Typeflag == tar.TypeDir
		switch {
		case h.Typeflag != tar.TypeReg && !dir:
			return nil, fmt.Errorf("the image tar's %q is no plain file or folder", name)
		case strings.HasPrefix(name, "/") || slices.Contains(strings.Split(name, "/"), "..") || path.Clean(name) != strings.TrimSuffix(name, "/"):
			return nil, fmt.Errorf("the image tar's %q leads out of it", name)
		case !knownEntry(name, dir):
			return nil, fmt.Errorf("the image tar's %q is no part of an image Docker saved", name)
		case seen[name]:
			return nil, fmt.Errorf("the image tar holds %q twice", name)
		}
		seen[name] = true
		if name == "manifest.json" || name == "index.json" {
			raw, err := io.ReadAll(io.LimitReader(tr, maxMetaFile+1))
			if err != nil || len(raw) > maxMetaFile {
				return nil, fmt.Errorf("the image tar's %s is larger than the cleaner reads", name)
			}
			meta[name] = raw
		}
	}
	if meta["manifest.json"] == nil {
		return nil, errors.New("the image tar has no manifest.json")
	}
	return meta, nil
}

// cleanIndex takes from index.json the names of its one manifest, and gives that manifest's digest.
func cleanIndex(raw []byte) ([]byte, string, error) {
	var idx map[string]json.RawMessage
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil, "", errors.New("the image tar's index.json is not one")
	}
	var mans []map[string]json.RawMessage
	if err := json.Unmarshal(idx["manifests"], &mans); err != nil || len(mans) != 1 {
		return nil, "", errors.New("the image tar's index.json is not one image")
	}
	var err error
	if a, ok := mans[0]["annotations"]; ok {
		var notes map[string]string
		if err := json.Unmarshal(a, &notes); err != nil {
			return nil, "", errors.New("the image tar's index.json has annotations that are not")
		}
		delete(notes, "io.containerd.image.name")
		delete(notes, "org.opencontainers.image.ref.name")
		if len(notes) == 0 {
			delete(mans[0], "annotations")
		} else if mans[0]["annotations"], err = json.Marshal(notes); err != nil {
			return nil, "", err
		}
	}
	var digest string
	if err := json.Unmarshal(mans[0]["digest"], &digest); err != nil {
		return nil, "", errors.New("the image tar's index.json names its manifest by no digest")
	}
	if idx["manifests"], err = json.Marshal(mans); err != nil {
		return nil, "", err
	}
	out, err := json.Marshal(idx)
	return out, digest, err
}

// readConfig reads what an image's config asks of the machine.
func readConfig(raw []byte) (Image, error) {
	var c struct {
		Config struct {
			Volumes      map[string]struct{} `json:"Volumes"`
			ExposedPorts map[string]struct{} `json:"ExposedPorts"`
			Labels       map[string]string   `json:"Labels"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return Image{}, errors.New("the image's config is not one")
	}
	var img Image
	for v := range c.Config.Volumes {
		img.Volumes = append(img.Volumes, v)
	}
	for p := range c.Config.ExposedPorts {
		img.Ports = append(img.Ports, p)
	}
	img.Labels = crewLabels(slices.Collect(maps.Keys(c.Config.Labels)))
	slices.Sort(img.Volumes)
	slices.Sort(img.Ports)
	return img, nil
}

// crewLabels are the names among an image's labels that are Crew's own, sorted.
func crewLabels(names []string) []string {
	var own []string
	for _, l := range names {
		if strings.HasPrefix(strings.ToLower(l), "crew.") {
			own = append(own, l)
		}
	}
	slices.Sort(own)
	return own
}

// ConfigProblem says why an image cannot be hosted with these settings, or "" (plan M19 D31): a VOLUME
// other than the data folder would be an anonymous volume, growing on the machine's disk with no
// bound; ports exposed without hosting.port say the image serves elsewhere.
func ConfigProblem(img Image, port int, data string) string {
	// Crew's own labels name what a container is to the orchestrator: an image does not choose them
	// (security review of M19).
	if len(img.Labels) > 0 {
		return "the image sets Crew's own labels, " + strings.Join(img.Labels, ", ")
	}
	for _, v := range img.Volumes {
		if path.Clean(v) != path.Clean(data) {
			return fmt.Sprintf("the image declares the VOLUME %s, and only hosting.data (%s) is kept: an anonymous volume would grow on the machine's disk", v, data)
		}
	}
	if len(img.Ports) > 0 && !slices.Contains(img.Ports, strconv.Itoa(port)+"/tcp") {
		return fmt.Sprintf("the image exposes %s and not hosting.port %d", strings.Join(img.Ports, ", "), port)
	}
	return ""
}
