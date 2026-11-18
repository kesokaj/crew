package versions

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type fakeDocker struct {
	calls     []string
	failStart string
	created   *container.Config
}

func (f *fakeDocker) ContainerInspect(context.Context, string) (container.InspectResponse, error) {
	f.calls = append(f.calls, "inspect")
	return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: "old", HostConfig: &container.HostConfig{NetworkMode: "none"}},
		Config: &container.Config{Image: "crew-updater:0.1.0", Env: []string{"CREW_GID=999"}, Labels: map[string]string{"com.docker.compose.service": "crew-updater"}}}, nil
}
func (f *fakeDocker) ContainerRename(_ context.Context, id, name string) error {
	f.calls = append(f.calls, "rename "+id+" "+name)
	return nil
}
func (f *fakeDocker) ContainerStop(_ context.Context, id string, _ container.StopOptions) error {
	f.calls = append(f.calls, "stop "+id)
	return nil
}
func (f *fakeDocker) ContainerCreate(_ context.Context, cfg *container.Config, h *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, name string) (container.CreateResponse, error) {
	f.calls = append(f.calls, "create "+name+" "+cfg.Image+" "+string(h.NetworkMode))
	f.created = cfg
	return container.CreateResponse{ID: "new"}, nil
}
func (f *fakeDocker) ContainerStart(_ context.Context, id string, _ container.StartOptions) error {
	f.calls = append(f.calls, "start "+id)
	if id == f.failStart {
		return errors.New("exited 1")
	}
	return nil
}
func (f *fakeDocker) ContainerRemove(_ context.Context, id string, _ container.RemoveOptions) error {
	f.calls = append(f.calls, "remove "+id)
	return nil
}
func (f *fakeDocker) ImageInspect(_ context.Context, img string, _ ...client.ImageInspectOption) (image.InspectResponse, error) {
	f.calls = append(f.calls, "image "+img)
	return image.InspectResponse{}, nil
}

// The new updater gets the old one's settings with the new image; one that does not start is
// removed, and the old one runs again (16.8).
func TestReplacingTheUpdater(t *testing.T) {
	f := &fakeDocker{}
	if err := (DockerReplacer{Docker: f}).ReplaceUpdater(context.Background(), "0.2.0"); err != nil {
		t.Fatal(err)
	}
	want := "image crew-updater:0.2.0|inspect|rename old crew-updater-before|stop old|create crew-updater crew-updater:0.2.0 none|start new|remove old"
	if got := strings.Join(f.calls, "|"); got != want {
		t.Fatalf("%s\nwant %s", got, want)
	}
	if f.created.Env[0] != "CREW_GID=999" || f.created.Labels["com.docker.compose.service"] != "crew-updater" || f.created.Labels["crew.version"] != "0.2.0" {
		t.Fatalf("%+v", f.created)
	}
	f = &fakeDocker{failStart: "new"}
	err := (DockerReplacer{Docker: f}).ReplaceUpdater(context.Background(), "0.2.0")
	if err == nil || !strings.Contains(err.Error(), "the one before runs again") || !strings.HasSuffix(strings.Join(f.calls, "|"), "remove crew-updater|rename old crew-updater|start old") {
		t.Fatalf("%v %v", err, f.calls)
	}
}

type fakePush struct {
	calls  []string
	stream string
	auth   string
}

func (f *fakePush) ImageTag(_ context.Context, src, dst string) error {
	f.calls = append(f.calls, "tag "+src+" "+dst)
	return nil
}
func (f *fakePush) ImagePush(_ context.Context, ref string, o image.PushOptions) (io.ReadCloser, error) {
	f.calls = append(f.calls, "push "+ref)
	f.auth = o.RegistryAuth
	return io.NopCloser(strings.NewReader(f.stream)), nil
}
func (f *fakePush) ImageRemove(_ context.Context, ref string, _ image.RemoveOptions) ([]image.DeleteResponse, error) {
	f.calls = append(f.calls, "rm "+ref)
	return nil, nil
}

// A push gives the login for that push only, reads the stream for a failure, never tells the token,
// and takes the registry's name off the local image again.
func TestAPushToTheRegistry(t *testing.T) {
	f := &fakePush{stream: `{"status":"Pushed"}` + "\n" + `{"status":"0.2.0: digest: sha256:abc"}` + "\n"}
	if err := (DockerRegistry{Docker: f}).Push(context.Background(), "crew-server:0.2.0", "ghcr.io/op/crew-server:0.2.0", "op", "gho_secret"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, "|") != "tag crew-server:0.2.0 ghcr.io/op/crew-server:0.2.0|push ghcr.io/op/crew-server:0.2.0|rm ghcr.io/op/crew-server:0.2.0" {
		t.Fatal(f.calls)
	}
	raw, _ := base64.URLEncoding.DecodeString(f.auth)
	if !strings.Contains(string(raw), `"password":"gho_secret"`) || !strings.Contains(string(raw), `"serveraddress":"ghcr.io"`) {
		t.Fatalf("%s", raw)
	}
	f = &fakePush{stream: `{"errorDetail":{"message":"denied: token gho_secret has no write:packages"},"error":"denied"}` + "\n"}
	err := (DockerRegistry{Docker: f}).Push(context.Background(), "crew-server:0.2.0", "ghcr.io/op/crew-server:0.2.0", "op", "gho_secret")
	if err == nil || !strings.Contains(err.Error(), "denied") || strings.Contains(err.Error(), "gho_secret") {
		t.Fatalf("%v", err)
	}
}
