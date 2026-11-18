package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
)

// Pause holds a hosted microVM still while its data is copied (plan M19 D18), after the check of
// whose it is, and gives what undoes it: an unpause of the container the check read, by its ID, which
// Docker gives no other container. So the unpause needs no check of its own, which could error and
// leave a live app paused, nothing trying again (review of the release of M20, round 2), and nothing
// can unpause by a name (security reviews of b39f701, finding 2, and of ff41a0f, finding 1).
func (o *Orchestrator) Pause(ctx context.Context, id string) (func(context.Context) error, error) {
	cid, err := o.sandboxTarget(ctx, SandboxName(id))
	if err != nil {
		return nil, err
	}
	if err := o.docker.ContainerPause(ctx, cid); err != nil {
		return nil, err
	}
	return func(ctx context.Context) error { return o.docker.ContainerUnpause(ctx, cid) }, nil
}

// Restart restarts a hosted microVM that stopped answering (plan M19 D11).
func (o *Orchestrator) Restart(ctx context.Context, id string) error {
	cid, err := o.sandboxTarget(ctx, SandboxName(id))
	if err != nil {
		return err
	}
	t := 10
	return o.docker.ContainerRestart(ctx, cid, container.StopOptions{Timeout: &t})
}

// Stop stops a hosted microVM, which keeps it for a roll back's put back of data (plan M19 D15).
func (o *Orchestrator) StopHosted(ctx context.Context, id string) error {
	cid, err := o.sandboxTarget(ctx, SandboxName(id))
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	t := 10
	err = o.docker.ContainerStop(ctx, cid, container.StopOptions{Timeout: &t})
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}

// HostedRunning says whether a hosted microVM runs; another server's is not read (review of 5ddd59a,
// as BUG-012's guard on every action by name).
func (o *Orchestrator) HostedRunning(ctx context.Context, id string) (bool, error) {
	cid, err := o.sandboxTarget(ctx, SandboxName(id))
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return o.Running(ctx, cid)
}

// StartAgain starts a stopped hosted microVM again.
func (o *Orchestrator) StartAgain(ctx context.Context, id string) error {
	cid, err := o.sandboxTarget(ctx, SandboxName(id))
	if err != nil {
		return err
	}
	return o.docker.ContainerStart(ctx, cid, container.StartOptions{})
}

// MaxHostedLog bounds what one read of a hosted microVM's log takes.
const MaxHostedLog = 8 << 20

// HostedLogs is what a hosted microVM printed since a time, stdout and stderr in the order written,
// at most MaxHostedLog bytes, with Docker's time stamps. Another server's is not read.
func (o *Orchestrator) HostedLogs(ctx context.Context, id string, since time.Time) ([]byte, error) {
	cid, err := o.sandboxTarget(ctx, SandboxName(id))
	if err != nil {
		return nil, err
	}
	rc, err := o.docker.ContainerLogs(ctx, cid, container.LogsOptions{ShowStdout: true, ShowStderr: true,
		Timestamps: true, Since: strconv.FormatInt(since.Unix(), 10)})
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var out bytes.Buffer
	w := &capWriter{w: &out, left: MaxHostedLog}
	if _, err := stdcopy.StdCopy(w, w, rc); err != nil && !errors.Is(err, errFull) {
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}

var errFull = errors.New("full")

// capWriter takes at most left bytes and then says it is full.
type capWriter struct {
	w    io.Writer
	left int
}

func (c *capWriter) Write(p []byte) (int, error) {
	if len(p) > c.left {
		n, _ := c.w.Write(p[:c.left])
		c.left = 0
		return n, errFull
	}
	c.left -= len(p)
	return c.w.Write(p)
}
