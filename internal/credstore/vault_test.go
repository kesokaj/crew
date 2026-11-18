package credstore_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kesokaj/crew/internal/credstore"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/db/dbtest"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, credstore.KeySize) }

func TestVaultKeepsACredentialSealed(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	v, err := credstore.NewVault(d.Server, key(1))
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("sk-made-up-token-for-the-test")
	if err := v.Put(ctx, credstore.Credential{ID: credstore.GH, Kind: "value", Data: secret}, "test"); err != nil {
		t.Fatal(err)
	}

	got, err := v.Get(ctx, credstore.GH)
	if err != nil || !bytes.Equal(got.Data, secret) {
		t.Fatalf("Get = %q, %v", got.Data, err)
	}

	// The row holds no plain text, and neither does the history.
	var sealed []byte
	if err := d.Server.QueryRow(ctx, `SELECT sealed FROM credentials WHERE id = $1`, credstore.GH).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, secret) {
		t.Fatal("the stored value holds the secret in plain text")
	}
	hist, err := db.History(ctx, d.Server, "credentials", credstore.GH)
	if err != nil || len(hist) != 1 {
		t.Fatalf("history = %v, %v", hist, err)
	}
	if strings.Contains(string(hist[0].After), string(secret)) || hist[0].Who != "test" {
		t.Fatalf("the history holds %s by %s", hist[0].After, hist[0].Who)
	}

	// Without the key file it cannot be read (design 22, Credentials).
	other, _ := credstore.NewVault(d.Server, key(2))
	if _, err := other.Get(ctx, credstore.GH); err == nil {
		t.Fatal("another key opened the credential")
	}
}

func TestVaultRefusesASealedValueMovedToAnotherRow(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	v, _ := credstore.NewVault(d.Server, key(1))
	if err := v.Put(ctx, credstore.Credential{ID: credstore.Agy, Kind: "file", Data: []byte("agy")}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := v.Put(ctx, credstore.Credential{ID: credstore.GH, Kind: "value", Data: []byte("gh")}, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Server.Exec(ctx, `UPDATE credentials SET nonce = a.nonce, sealed = a.sealed
		FROM credentials a WHERE credentials.id = $1 AND a.id = $2`, credstore.GH, credstore.Agy); err != nil {
		t.Fatal(err)
	}
	if c, err := v.Get(ctx, credstore.GH); err == nil {
		t.Fatalf("a value sealed for agy opened as gh: %q", c.Data)
	}
}

func TestVaultReplaceListAndDelete(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	v, _ := credstore.NewVault(d.Server, key(1))
	for _, data := range []string{"one", "two"} {
		if err := v.Put(ctx, credstore.Credential{ID: credstore.Agy, Kind: "file", Account: "agy@example.com", Data: []byte(data)}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := v.Get(ctx, credstore.Agy)
	if string(got.Data) != "two" || got.Account != "agy@example.com" {
		t.Fatalf("after a replace: %q %q", got.Data, got.Account)
	}
	list, err := v.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != credstore.Agy || list[0].Account != "agy@example.com" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if err := v.Delete(ctx, credstore.Agy, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Get(ctx, credstore.Agy); !errors.Is(err, credstore.ErrNotFound) {
		t.Fatalf("after Delete, Get = %v", err)
	}
	if err := v.Delete(ctx, credstore.Agy, "test"); err != nil {
		t.Fatalf("a second Delete: %v", err)
	}
	hist, _ := db.History(ctx, d.Server, "credentials", credstore.Agy)
	if len(hist) != 3 || hist[2].After != nil {
		t.Fatalf("history: %d rows", len(hist))
	}
}

func TestVaultRefusesEmptyAndTooLarge(t *testing.T) {
	ctx := context.Background()
	d := dbtest.New(t, false)
	v, _ := credstore.NewVault(d.Server, key(1))
	if err := v.Put(ctx, credstore.Credential{ID: credstore.Agy, Kind: "file"}, "test"); err == nil {
		t.Fatal("an empty credential was stored")
	}
	if err := v.Put(ctx, credstore.Credential{ID: credstore.Agy, Kind: "file", Data: make([]byte, credstore.MaxSize+1)}, "test"); err == nil {
		t.Fatal("a credential over the size limit was stored")
	}
}

func TestReadKey(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	os.WriteFile(good, key(3), 0o400)
	if k, err := credstore.ReadKey(good); err != nil || len(k) != credstore.KeySize {
		t.Fatalf("ReadKey = %d bytes, %v", len(k), err)
	}
	short := filepath.Join(dir, "short")
	os.WriteFile(short, []byte("short"), 0o400)
	for _, p := range []string{short, filepath.Join(dir, "missing")} {
		if _, err := credstore.ReadKey(p); !errors.Is(err, credstore.ErrNoKey) {
			t.Fatalf("ReadKey(%s) = %v", filepath.Base(p), err)
		}
	}
	if _, err := credstore.NewVault(nil, []byte("short")); !errors.Is(err, credstore.ErrNoKey) {
		t.Fatalf("NewVault with a short key = %v", err)
	}
}

// A terminal's log is sealed for its purpose: it opens with the key and that purpose only, and holds
// none of what it sealed in the clear.
func TestSealOpensOnlyForItsPurpose(t *testing.T) {
	key := make([]byte, credstore.KeySize)
	key[0] = 7
	v, err := credstore.NewVault(nil, key)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := v.Seal("terminal_log", []byte("cat token: secret-abc"))
	if err != nil || bytes.Contains(blob, []byte("secret-abc")) {
		t.Fatalf("sealed %q %v", blob, err)
	}
	if got, err := v.Open("terminal_log", blob); err != nil || string(got) != "cat token: secret-abc" {
		t.Fatalf("opened %q %v", got, err)
	}
	if _, err := v.Open("another", blob); err == nil {
		t.Error("opened for another purpose")
	}
	other, _ := credstore.NewVault(nil, make([]byte, credstore.KeySize))
	if _, err := other.Open("terminal_log", blob); err == nil {
		t.Error("opened with another key")
	}
	if _, err := v.Open("terminal_log", blob[:3]); err == nil {
		t.Error("opened a short blob")
	}
}
