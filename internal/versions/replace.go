package versions

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// UpdaterContainer is the updater's container.
const UpdaterContainer = "crew-updater"

// docker is what replacing the updater needs of Docker.
type docker interface {
	ContainerInspect(ctx context.Context, containerID string) (container.InspectResponse, error)
	ContainerRename(ctx context.Context, containerID, newContainerName string) error
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ImageInspect(ctx context.Context, imageID string, inspectOpts ...client.ImageInspectOption) (image.InspectResponse, error)
}

// DockerReplacer replaces the updater's container with one of a new image, with the same settings:
// the server's part of 16.8. The updater loaded the image at its step 2. A new updater that does not
// start is removed, and the old one starts again.
type DockerReplacer struct{ Docker docker }

// ReplaceUpdater runs the updater of version in place of the one that runs.
func (r DockerReplacer) ReplaceUpdater(ctx context.Context, version string) error {
	img := "crew-updater:" + version
	if _, err := r.Docker.ImageInspect(ctx, img); err != nil {
		return fmt.Errorf("the image %s: %w", img, err)
	}
	old, err := r.Docker.ContainerInspect(ctx, UpdaterContainer)
	if err != nil {
		return err
	}
	if old.Config == nil || old.HostConfig == nil {
		return fmt.Errorf("%s: its settings cannot be read", UpdaterContainer)
	}
	cfg := *old.Config
	cfg.Image = img
	labels := map[string]string{}
	for k, v := range cfg.Labels {
		labels[k] = v
	}
	labels["crew.version"] = version
	cfg.Labels = labels
	const aside = UpdaterContainer + "-before"
	if err := r.Docker.ContainerRename(ctx, old.ID, aside); err != nil {
		return err
	}
	back := func(cause error) error {
		c := context.WithoutCancel(ctx)
		_ = r.Docker.ContainerRemove(c, UpdaterContainer, container.RemoveOptions{Force: true})
		_ = r.Docker.ContainerRename(c, old.ID, UpdaterContainer)
		_ = r.Docker.ContainerStart(c, old.ID, container.StartOptions{})
		return fmt.Errorf("the updater of %s did not start, and the one before runs again: %w", version, cause)
	}
	timeout := 20
	if err := r.Docker.ContainerStop(ctx, old.ID, container.StopOptions{Timeout: &timeout}); err != nil {
		return back(err)
	}
	created, err := r.Docker.ContainerCreate(ctx, &cfg, old.HostConfig, nil, nil, UpdaterContainer)
	if err != nil {
		return back(err)
	}
	if err := r.Docker.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return back(err)
	}
	return r.Docker.ContainerRemove(ctx, old.ID, container.RemoveOptions{})
}
