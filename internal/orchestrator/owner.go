package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
)

// A server acts only on the sandboxes it started (D44): one another server on the same Docker
// started, as a test's throwaway server whose database came from this one's does, is never run in,
// read, stopped, started, paused nor removed by it. Each action by a sandbox's name reads whose it is
// and acts on the container it read, by its ID, so one another server makes under the name between
// the check and the action is not touched (security reviews of 54f60eb); its bridge too, by the
// bridge's own labels and ID (security review of b39f701, finding 1).

// ErrAnothers is what a change of a sandbox another server started gives, and every action that takes
// only a sandbox's name (sandboxTarget) or uses its bridge, of what under the name has no sandbox
// label.
var ErrAnothers = errors.New("the sandbox is another server's")

// sandboxTarget is the ID of the container of a sandbox's name, read with the check of whose it is,
// for every action on a sandbox: a removal, a stop, a start, a restart, a pause, an adoption, the
// ingress's join, the reads of its log and whether it runs, and a command in it. A container Docker
// does not have is known by its bridge, which has the same name and labels: one that is not this
// server's sandbox is ErrAnothers, else the inspect's not-found error stands. A check that errors
// gives its error, so nothing goes on by name when it cannot tell (security review of the release of
// M20). Every sandbox Crew makes has its labels (since M2), so what under the name has none is not
// this server's (security review of the release of 0.3.3, findings 1 and 3, and its extensions over
// ff1e7fd, finding 2, and over 325ff81, finding 1). Since 0.84 no action counts a container with no
// sandbox label as this server's: the copied project code that ran commands in such containers is
// gone (security review of bd427d1, finding 2).
func (o *Orchestrator) sandboxTarget(ctx context.Context, name string) (string, error) {
	insp, err := o.docker.ContainerInspect(ctx, name)
	if errdefs.IsNotFound(err) {
		n, nerr := o.docker.NetworkInspect(ctx, name, network.InspectOptions{})
		if nerr == nil && o.notOurs(n.Labels) {
			return "", ErrAnothers
		}
		if nerr != nil && !errdefs.IsNotFound(nerr) {
			return "", fmt.Errorf("reading whose the bridge %s is: %w", name, nerr)
		}
	}
	if err != nil {
		return "", fmt.Errorf("reading whose the container %s is: %w", name, err)
	}
	var labels map[string]string
	if insp.Config != nil {
		labels = insp.Config.Labels
	}
	if o.notOurs(labels) {
		return "", ErrAnothers
	}
	return insp.ID, nil
}

// notOurs says whether labels are no sandbox of this server's: another server's, or none.
func (o *Orchestrator) notOurs(labels map[string]string) bool {
	return !o.labelledOurs(labels)
}

// labelledOurs says whether labels name a sandbox of this server's.
func (o *Orchestrator) labelledOurs(labels map[string]string) bool {
	_, sandbox := labels[LabelSandbox]
	return sandbox && o.ours(labels)
}
