package release

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"regexp"
	"slices"
	"strings"
)

// An image's id is not the same on every machine (D48): Docker's classic image store gives the digest
// of its config, the containerd store, which a fresh Docker 29 has, the digest of the manifest it was
// pulled by or loaded from. The config digest is the one both keep, so release.json names it, and a
// check takes the config digest or a manifest that names it.

// digestShape is a sha256 digest as Docker writes one.
var digestShape = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ImageFile is what a file docker save wrote says of the image in it.
type ImageFile struct {
	// Config is the digest of its config, which manifest.json names; empty when it names none.
	Config string
	// IDs are the ids Docker may give the image once loaded: the config digest, and the digest of each
	// manifest in index.json that names that config, or of an index one of whose manifests does.
	IDs []string
}

// smallBlob bounds each blob read into memory, and allBlobs all of them: a manifest or an index is a
// few kB, and a layer above the bound is not read at all.
const (
	smallBlob = 1 << 20
	allBlobs  = 64 << 20
)

// ReadImageFile reads the file at path, which docker save wrote, once from start to end. It does not
// hash the layers: Verify checks the whole file by release.json's hash. A file that is no tar from its
// first block says nothing, and docker load refuses it.
func ReadImageFile(path string) (ImageFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return ImageFile{}, err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	var configs []string
	var index []descriptor
	blobs := map[string][]byte{}
	kept := 0
	for first := true; ; first = false {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if first && (errors.Is(err, tar.ErrHeader) || errors.Is(err, io.ErrUnexpectedEOF)) {
			return ImageFile{}, nil
		}
		if err != nil {
			return ImageFile{}, err
		}
		name := pathpkg.Clean(strings.TrimLeft(hdr.Name, "/"))
		switch {
		case name == "manifest.json":
			var m []struct {
				Config string `json:"Config"`
			}
			if err := json.NewDecoder(io.LimitReader(tr, smallBlob)).Decode(&m); err != nil {
				return ImageFile{}, fmt.Errorf("manifest.json: %w", err)
			}
			// An entry that names no config is no image docker load takes.
			for _, e := range m {
				if e.Config != "" {
					configs = append(configs, e.Config)
				}
			}
		case name == "index.json":
			var idx struct {
				Manifests []descriptor `json:"manifests"`
			}
			if err := json.NewDecoder(io.LimitReader(tr, smallBlob)).Decode(&idx); err != nil {
				return ImageFile{}, fmt.Errorf("index.json: %w", err)
			}
			index = idx.Manifests
		case strings.HasPrefix(name, "blobs/sha256/") && hdr.Typeflag == tar.TypeReg && hdr.Size <= smallBlob && kept+int(hdr.Size) <= allBlobs:
			b, err := io.ReadAll(io.LimitReader(tr, smallBlob))
			if err != nil {
				return ImageFile{}, err
			}
			kept += len(b)
			blobs["sha256:"+strings.TrimPrefix(name, "blobs/sha256/")] = b
		}
	}
	var out ImageFile
	switch len(configs) {
	case 0:
		return out, nil
	case 1:
	default:
		return out, fmt.Errorf("the file holds %d images: a release's file holds one", len(configs))
	}
	// manifest.json names the config as a path: blobs/sha256/HEX since Docker 25, HEX.json before.
	c := configs[0]
	switch {
	case strings.HasPrefix(c, "blobs/sha256/"):
		c = "sha256:" + strings.TrimPrefix(c, "blobs/sha256/")
	case strings.HasSuffix(c, ".json") && !strings.Contains(c, "/"):
		c = "sha256:" + strings.TrimSuffix(c, ".json")
	}
	if !digestShape.MatchString(c) {
		return out, fmt.Errorf("manifest.json names the config %q", configs[0])
	}
	out.Config, out.IDs = c, []string{c}
	for _, d := range index {
		if d.names(c, blobs, 0) && !slices.Contains(out.IDs, d.Digest) {
			out.IDs = append(out.IDs, d.Digest)
		}
	}
	return out, nil
}

// descriptor is an entry of an OCI index.
type descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Platform    map[string]any    `json:"platform"`
	Annotations map[string]string `json:"annotations"`
}

// platform says what an index's entry is to Docker here: "amd64" for exactly linux/amd64, with no
// variant or any other field; "like" for any other entry Docker could take for linux/amd64 once it
// normalised it (case, x86_64, a variant, an empty field, or no platform at all); "other" for another
// platform, such as arm64, or an attestation's unknown/unknown. An index with a "like" entry is
// refused (security review of M17's Close, round 2).
func (d descriptor) platform() string {
	if len(d.Platform) == 2 && d.Platform["os"] == "linux" && d.Platform["architecture"] == "amd64" {
		return "amd64"
	}
	os, _ := d.Platform["os"].(string)
	arch, _ := d.Platform["architecture"].(string)
	os, arch = strings.ToLower(os), strings.ToLower(arch)
	if (os == "linux" || os == "") && slices.Contains([]string{"amd64", "x86_64", "x86-64", ""}, arch) {
		return "like"
	}
	return "other"
}

// names reports whether the manifest or index d points at, by a blob in the file that has its
// digest, names the config c. An index is followed one level: an image with its attestations. It
// holds one manifest for exactly linux/amd64, which must be the one that names c, and no other entry
// Docker could take for linux/amd64: Docker runs the manifest of its platform, so an index with
// another could run what nobody checked, as release pull refuses (security review of M17's Close).
func (d descriptor) names(c string, blobs map[string][]byte, depth int) bool {
	b, ok := blobs[d.Digest]
	if !ok || !digestShape.MatchString(d.Digest) {
		return false
	}
	sum := sha256.Sum256(b)
	if "sha256:"+hex.EncodeToString(sum[:]) != d.Digest {
		return false
	}
	var m struct {
		Config    descriptor   `json:"config"`
		Manifests []descriptor `json:"manifests"`
	}
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	if m.Config.Digest == c {
		return true
	}
	if depth > 0 {
		return false
	}
	var amd []descriptor
	for _, child := range m.Manifests {
		switch child.platform() {
		case "amd64":
			amd = append(amd, child)
		case "like":
			return false
		}
	}
	return len(amd) == 1 && amd[0].names(c, blobs, depth+1)
}

// AcceptsID reports whether Docker's id for an image loaded from this file is the image release.json
// names by its config digest id: the config digest itself, or a manifest of the file that names it.
// A file whose manifest.json names another config is the file of another image, whatever id it got.
func (f ImageFile) AcceptsID(id, got string) bool {
	if f.Config == "" {
		return got == id
	}
	return f.Config == id && slices.Contains(f.IDs, got)
}
