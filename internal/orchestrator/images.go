package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
)

// LoadImage loads an image tar into Docker and returns the id Docker says it loaded. The tar is the
// cleaned one (plan M19 D1): it names no image, so the id is all Docker says.
func (o *Orchestrator) LoadImage(ctx context.Context, r io.Reader) (string, error) {
	resp, err := o.docker.ImageLoad(ctx, r, client.ImageLoadWithQuiet(true))
	if err != nil {
		return "", fmt.Errorf("loading the image: %w", err)
	}
	defer resp.Body.Close()
	var id string
	dec := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	for {
		var m struct {
			Stream string `json:"stream"`
			Error  string `json:"error"`
		}
		if err := dec.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			return "", fmt.Errorf("Docker's answer to the load: %w", err)
		}
		if m.Error != "" {
			return "", fmt.Errorf("loading the image: %s", cutWhy(m.Error))
		}
		for _, line := range strings.Split(m.Stream, "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Loaded image ID: "); ok {
				if id != "" {
					return "", errors.New("Docker loaded more than one image")
				}
				id = rest
			} else if strings.HasPrefix(strings.TrimSpace(line), "Loaded image: ") {
				return "", errors.New("Docker loaded an image by a name, which a cleaned tar has none of")
			}
		}
	}
	if id == "" {
		return "", errors.New("Docker did not say which image it loaded")
	}
	return id, nil
}

// TagImage names an image, by its id, crew-host/<project id>:<deploy number> (plan M19 D30).
func (o *Orchestrator) TagImage(ctx context.Context, id, ref string) error {
	return o.docker.ImageTag(ctx, id, ref)
}

// RemoveImage removes a tag of a hosted project's image, and the image when no other tag names it;
// one that is gone is no error.
func (o *Orchestrator) RemoveImage(ctx context.Context, ref string) error {
	_, err := o.docker.ImageRemove(ctx, ref, image.RemoveOptions{PruneChildren: true})
	if err != nil && !errdefs.IsNotFound(err) {
		return err
	}
	return nil
}

// HasImage reports whether Docker holds the image.
func (o *Orchestrator) HasImage(ctx context.Context, ref string) (bool, error) {
	_, err := o.docker.ImageInspect(ctx, ref)
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// ImageConfig is what an image Docker holds asks of the machine: its VOLUMEs, its EXPOSEd ports, and
// the names of its labels.
func (o *Orchestrator) ImageConfig(ctx context.Context, ref string) (volumes, ports, labels []string, err error) {
	insp, err := o.docker.ImageInspect(ctx, ref)
	if err != nil {
		return nil, nil, nil, err
	}
	if insp.Config != nil {
		for v := range insp.Config.Volumes {
			volumes = append(volumes, v)
		}
		for p := range insp.Config.ExposedPorts {
			ports = append(ports, p)
		}
		for l := range insp.Config.Labels {
			labels = append(labels, l)
		}
	}
	slices.Sort(volumes)
	slices.Sort(ports)
	slices.Sort(labels)
	return volumes, ports, labels, nil
}
