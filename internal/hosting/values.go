package hosting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/board"
	"github.com/kesokaj/crew/internal/capacity"
	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/harness"
	"github.com/kesokaj/crew/internal/orchestrator"
)

// Service hosts the projects of this machine.
type Service struct {
	Pool       *pgxpool.Pool
	Vault      *credstore.Vault
	AppsDomain string // CREW_APPS_DOMAIN
	CrewHost   string // CREW_HOSTNAME
	Log        *slog.Logger
	// Runtime starts and joins the hosted sandboxes: the orchestrator, a fake in a test.
	Runtime Runtime
	// Builder builds a project's image in a sandbox with no agent; Git reads the project's main.
	Builder harness.ImageBuilder
	Git     Git
	// Room is the capacity check (13.10); nil in a test, where there is room.
	Room *capacity.Gate
	// Dir is <data>/hosted, each project's data, copies and logs; Builds is <data>/builds.
	Dir, Builds string
	// Base is the server's life: a deploy is carried on in it, whoever asked.
	Base context.Context
	// Tickets adds the ticket that makes a project hostable to its board (plan M19 D14).
	Tickets func(ctx context.Context, projectID string, n board.NewTicket) (TicketMade, error)
	// HealthClient asks the health addresses; nil is one with no proxy and no redirect.
	HealthClient *http.Client
	// Now is the clock of health; nil is time.Now.
	Now func() time.Time

	run running
	wg  sync.WaitGroup
	hl  health
	// logsAt is, for each sandbox, the time of the last line of it copied into its log (D17).
	logsMu sync.Mutex
	logsAt map[string]time.Time
}

// TicketMade is a ticket the board added.
type TicketMade struct {
	ID  int64
	Key string
}

// Runtime is what hosting asks of the orchestrator.
type Runtime interface {
	JoinIngress(ctx context.Context, id string) error
	CreateHostedNet(ctx context.Context, id string, internet bool) (orchestrator.HostedNet, error)
	ProbeHosted(ctx context.Context, id string, n orchestrator.HostedNet) error
	StartHosted(ctx context.Context, spec orchestrator.HostedSpec) (orchestrator.Sandbox, error)
	RemoveSandbox(ctx context.Context, id string) error
	LoadImage(ctx context.Context, r io.Reader) (string, error)
	TagImage(ctx context.Context, id, ref string) error
	RemoveImage(ctx context.Context, ref string) error
	HasImage(ctx context.Context, ref string) (bool, error)
	ImageConfig(ctx context.Context, ref string) (volumes, ports, labels []string, err error)
	// Pause gives what undoes it.
	Pause(ctx context.Context, id string) (resume func(context.Context) error, err error)
	Restart(ctx context.Context, id string) error
	StopHosted(ctx context.Context, id string) error
	StartAgain(ctx context.Context, id string) error
	HostedRunning(ctx context.Context, id string) (bool, error)
	HostedLogs(ctx context.Context, id string, since time.Time) ([]byte, error)
}

// InputError is a request the service refuses, with the reason the operator reads.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// The bounds of a hosted project's values (plan M19, Interfaces): Crew's own names and PORT, which
// Crew sets, are not the operator's.
const (
	maxValues    = 50
	maxValueSize = 4 << 10
	valuesKind   = "hosting"
)

var valueName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)

// valuesID is the vault row of a project's values: one row, the whole set sealed (D7).
func valuesID(project string) string { return "host/" + project }

// refused says why a project of this name cannot be hosted on this machine, or "".
func (s *Service) refused(name string) string {
	if why := AppsDomainProblem(s.AppsDomain, s.CrewHost); why != "" {
		return why
	}
	return NameProblem(name)
}

// values reads a project's values; none is an empty set, and a row sealed with another key file is
// credstore.ErrUnreadable.
func (s *Service) values(ctx context.Context, project string) (map[string]string, error) {
	c, err := s.Vault.Get(ctx, valuesID(project))
	if errors.Is(err, credstore.ErrUnreadable) {
		return nil, err
	}
	if errors.Is(err, credstore.ErrNotFound) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if err := json.Unmarshal(c.Data, &out); err != nil {
		return nil, fmt.Errorf("the values of %s: %w", project, err)
	}
	return out, nil
}

// SetValues stores the whole set of a project's values; an empty set removes the row. Neither the
// values nor a word of them reaches an error or a log line.
func (s *Service) SetValues(ctx context.Context, project string, values map[string]string, who string) error {
	if len(values) > maxValues {
		return &InputError{fmt.Sprintf("at most %d values", maxValues)}
	}
	for k, v := range values {
		switch {
		case !valueName.MatchString(k):
			return &InputError{"a name is capital letters, digits and _, not beginning with a digit, at most 64"}
		case strings.HasPrefix(k, "CREW_") || k == "PORT":
			return &InputError{k + " is Crew's own: Crew sets it"}
		case len(v) > maxValueSize:
			return &InputError{fmt.Sprintf("the value of %s is more than %d bytes", k, maxValueSize)}
		}
	}
	if len(values) == 0 {
		return s.Vault.Delete(ctx, valuesID(project), who)
	}
	data, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return s.Vault.Put(ctx, credstore.Credential{ID: valuesID(project), Kind: valuesKind, Data: data}, who)
}
