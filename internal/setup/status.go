package setup

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/orchestrator"
)

// Item is one credential of Setup as the screen shows it: never its secret.
type Item struct {
	ID        string     `json:"id"`
	Set       bool       `json:"set"`
	Account   string     `json:"account,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	// Unreadable is a credential that is stored but does not open with this key file (21).
	Unreadable bool `json:"unreadable,omitempty"`
}

// Status is what the Setup screen shows.
type Status struct {
	Credentials []Item `json:"credentials"`
	// PublicKey is the public half of the SSH key, to add to GitHub.
	PublicKey string `json:"publicKey,omitempty"`
	// Running is the step whose setup microVM runs, or "".
	Running string `json:"running"`
	// Machine is the machine check: Docker, and Kata as its runtime.
	Machine *orchestrator.MachineCheck `json:"machine,omitempty"`
}

// Status reads what is set up.
func (s *Service) Status(ctx context.Context) (Status, error) {
	list, err := s.Vault.List(ctx)
	if err != nil {
		return Status{}, err
	}
	st := Status{Running: s.Running(), Credentials: []Item{}}
	if s.Machine != nil {
		if m, err := s.Machine(ctx); err == nil {
			st.Machine = &m
		} else {
			s.Log.Warn("the machine check", "error", err)
		}
	}
	for _, id := range credstore.Setup {
		it := Item{ID: id}
		i := slices.IndexFunc(list, func(c credstore.Info) bool { return c.ID == id })
		if i >= 0 {
			c := list[i]
			it.Set, it.Account, it.UpdatedAt, it.CreatedAt = true, c.Account, &c.UpdatedAt, &c.CreatedAt
			// A credential sealed with a lost key is set up again (21): Setup says so, and opens.
			if _, err := s.Vault.Get(ctx, id); errors.Is(err, credstore.ErrUnreadable) {
				it.Set, it.Unreadable = false, true
			} else if err != nil && !errors.Is(err, credstore.ErrNotFound) {
				return Status{}, err
			}
		}
		st.Credentials = append(st.Credentials, it)
	}
	if c, err := s.Vault.Get(ctx, credstore.SSHKey); err == nil {
		if files, err := credstore.ParseBundle(c.Data); err == nil {
			for _, f := range files {
				if f.Path == SSHPubFile {
					if line, ok := publicKeyLine(strings.TrimSpace(string(f.Data))); ok {
						st.PublicKey = line
					}
				}
			}
		}
	} else if !errors.Is(err, credstore.ErrNotFound) {
		return Status{}, err
	}
	return st, nil
}
