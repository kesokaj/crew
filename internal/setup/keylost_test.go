package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/credbroker"
	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/db/dbtest"
	"github.com/kesokaj/crew/internal/logins"
	"github.com/kesokaj/crew/internal/sandboxes"
)

// The key that encrypts secrets is lost (21): a server with a new key file finds the stored
// credentials unreadable. Setup still opens and shows each of them as one to set up again, and why;
// the CLIs read as not signed in, and a check refuses at once; a sandbox gets what can be read and
// starts; and a credential set up again with the new key reads as before.
func TestALostKeyAsksForTheCredentialsAgain(t *testing.T) {
	d := dbtest.New(t, false)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	old, err := credstore.NewVault(d.Server, bytes.Repeat([]byte{1}, credstore.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := json.Marshal(credstore.Identity{Name: "Ada", Email: "ada@example.com"})
	ssh, err := credstore.MakeBundle([]credstore.BundleFile{{Path: SSHPubFile, Mode: "0644", Data: []byte(pubLine)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []credstore.Credential{
		{ID: credstore.Agy, Kind: "file", Data: []byte(`{"token":"agy"}`)},
		{ID: credstore.GH, Kind: "file", Data: []byte("gh-login")},
		{ID: credstore.GitIdentity, Kind: "value", Data: id},
		{ID: credstore.SSHKey, Kind: "file", Data: ssh},
	} {
		if err := old.Put(ctx, c, "test"); err != nil {
			t.Fatal(err)
		}
	}
	vault, err := credstore.NewVault(d.Server, bytes.Repeat([]byte{2}, credstore.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Get(ctx, credstore.Agy); !errors.Is(err, credstore.ErrUnreadable) || !errors.Is(err, credstore.ErrNotFound) {
		t.Fatalf("a credential sealed with another key: %v", err)
	}

	s := &Service{Vault: vault, Log: log}
	st, err := s.Status(ctx)
	if err != nil {
		t.Fatalf("Setup does not open: %v", err)
	}
	unreadable := map[string]bool{credstore.Agy: true, credstore.GH: true, credstore.GitIdentity: true, credstore.SSHKey: true}
	for _, it := range st.Credentials {
		if it.Set || it.Unreadable != unreadable[it.ID] {
			t.Errorf("%s: set %v, unreadable %v", it.ID, it.Set, it.Unreadable)
		}
	}
	if st.PublicKey != "" {
		t.Errorf("a public key from an unreadable credential: %q", st.PublicKey)
	}

	l := &logins.Service{Pool: d.Server, Vault: vault, Log: log}
	states, err := l.States(ctx)
	if err != nil {
		t.Fatalf("the Agent CLIs screen does not open: %v", err)
	}
	for _, s := range states {
		if s.SetUp {
			t.Errorf("%s reads as set up", s.CLI)
		}
	}
	r, err := l.Check(ctx, "agy", false)
	if err != nil || r.State != cli.LoginRefused {
		t.Fatalf("a check of an unreadable login: %+v %v", r, err)
	}

	var said strings.Builder
	b := &credbroker.Broker{Vault: vault, Log: slog.New(slog.NewTextHandler(&said, nil))}
	c, err := b.Collect(ctx, sandboxes.Row{ID: "s1", Kind: sandboxes.KindRun, CLI: "agy"})
	if err != nil {
		t.Fatalf("a sandbox cannot start: %v", err)
	}
	if len(c.Files) != 0 || c.Env["CREW_GIT_NAME"] != "" {
		t.Fatalf("a sandbox got an unreadable credential: %+v", c)
	}
	if !strings.Contains(said.String(), "does not open with the key file") || !strings.Contains(said.String(), "credential=agy") {
		t.Fatalf("the broker left it out without a word: %s", said.String())
	}

	// Signing in again in Setup stores it with the new key.
	if err := vault.Put(ctx, credstore.Credential{ID: credstore.Agy, Kind: "file", Data: []byte(`{"token":"new"}`)}, "test"); err != nil {
		t.Fatal(err)
	}
	if st, err = s.Status(ctx); err != nil {
		t.Fatal(err)
	}
	for _, it := range st.Credentials {
		if it.ID == credstore.Agy && (!it.Set || it.Unreadable) {
			t.Fatalf("set up again: %+v", it)
		}
	}
	if c, err = b.Collect(ctx, sandboxes.Row{ID: "s1", Kind: sandboxes.KindRun, CLI: "agy"}); err != nil || len(c.Files) != 1 {
		t.Fatalf("the new login does not reach a sandbox: %+v %v", c, err)
	}
}
