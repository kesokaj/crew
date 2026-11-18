// Package credstore keeps the credentials of Setup in the database, sealed with the key file
// (design 12.6, 15.3).
package credstore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kesokaj/crew/internal/cli"
	"github.com/kesokaj/crew/internal/db"
	"github.com/kesokaj/crew/internal/homepath"
)

// The credentials of Setup, kept in the database and sealed with the key file (design 12.6, 15.3).
// A copy of the database alone shows no login: the key is in /etc/crew/secret.key, which is in
// neither the database nor a backup.

// The fixed names of what Setup keeps. Nothing else is a credential of the machine.
const (
	GitIdentity = "git-identity" // {"name", "email"}
	SSHKey      = "ssh-key"      // the private key, OpenSSH format
	GH          = "gh"           // gh's hosts.yml; stays with the server
	GitHubApp   = "github-app"   // {"appId", "key"}: the App's id and its private key
	Gcloud      = "gcloud"       // a bundle of gcloud's files
	Agy         = cli.AgyName    // agy's token file
	// Mail is the account Crew sends its mail with, {"account", "password"} (18.4). It is not in
	// Setup's list below, which the setup microVM holds, and no sandbox's grant names it (rules 6, 8).
	Mail = "mail"
)

// Setup lists the credentials of Setup, in the order Setup shows them.
var Setup = []string{GitIdentity, SSHKey, GH, GitHubApp, Gcloud, Agy}

// KeySize is the size of the key file: AES-256.
const KeySize = 32

// ErrNotFound is returned when a credential is not set up.
var ErrNotFound = errors.New("credential not found")

// ErrUnreadable is a credential sealed with another key file: the key was lost (21). It is also
// ErrNotFound, so every reader treats it as not set up and it is asked for again in Setup.
var ErrUnreadable = fmt.Errorf("%w: it was sealed with another key file", ErrNotFound)

// ErrNoKey is returned when the key file is missing or has the wrong size. The credentials cannot
// be read then, and the operator signs in again in Setup (design 21).
var ErrNoKey = errors.New("the key file that seals the credentials is missing or not 32 bytes")

// Credential is one stored credential. Data is the secret; the rest may be shown.
type Credential struct {
	ID        string
	Kind      string // "value", "file", or "bundle"
	Account   string // whose it is, when that is known: a Google address, a GitHub login, an App id
	Data      []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Info is a credential without its secret, for the screens.
type Info struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Account   string    `json:"account,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Vault keeps credentials in the database, sealed.
type Vault struct {
	pool *pgxpool.Pool
	aead cipher.AEAD
}

// ReadKey reads the key file. It must be exactly KeySize bytes.
func ReadKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoKey, err)
	}
	if len(key) != KeySize {
		return nil, ErrNoKey
	}
	return key, nil
}

// NewVault seals with key, which must be KeySize bytes.
func NewVault(pool *pgxpool.Pool, key []byte) (*Vault, error) {
	if len(key) != KeySize {
		return nil, ErrNoKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{pool: pool, aead: aead}, nil
}

// The id is sealed in with the value, so a sealed value moved to another row does not open.
func (v *Vault) seal(id string, data []byte) (nonce, sealed []byte, err error) {
	nonce = make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, v.aead.Seal(nil, nonce, data, []byte(id)), nil
}

func (v *Vault) open(id string, nonce, sealed []byte) ([]byte, error) {
	data, err := v.aead.Open(nil, nonce, sealed, []byte(id))
	if err != nil {
		return nil, fmt.Errorf("credential %s does not open with this key file: %w", id, ErrUnreadable)
	}
	return data, nil
}

// Seal encrypts data that is no credential but may show one, such as a terminal's log (13.11), for
// a purpose sealed in with it: the nonce, then the sealed bytes. It opens only with this key and
// purpose.
func (v *Vault) Seal(purpose string, data []byte) ([]byte, error) {
	nonce, sealed, err := v.seal("purpose:"+purpose, data)
	if err != nil {
		return nil, err
	}
	return append(nonce, sealed...), nil
}

// Open opens what Seal sealed for the same purpose.
func (v *Vault) Open(purpose string, blob []byte) ([]byte, error) {
	n := v.aead.NonceSize()
	if len(blob) < n {
		return nil, errors.New("sealed data too short")
	}
	return v.open("purpose:"+purpose, blob[:n], blob[n:])
}

// Put stores c, replacing what was stored under its id. who is recorded in the history, with the
// kind and the account, never the secret (6.4).
func (v *Vault) Put(ctx context.Context, c Credential, who string) error {
	if c.ID == "" || len(c.Data) == 0 {
		return errors.New("a credential needs an id and a value")
	}
	if len(c.Data) > MaxSize {
		return fmt.Errorf("credential %s is %d bytes, more than %d", c.ID, len(c.Data), MaxSize)
	}
	nonce, sealed, err := v.seal(c.ID, c.Data)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, v.pool, func(tx pgx.Tx) error {
		var before *Info
		var b Info
		err := tx.QueryRow(ctx, `SELECT id, kind, coalesce(account, ''), created_at, updated_at FROM credentials
			WHERE id = $1 FOR UPDATE`, c.ID).Scan(&b.ID, &b.Kind, &b.Account, &b.CreatedAt, &b.UpdatedAt)
		switch {
		case err == nil:
			before = &b
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		var after Info
		err = tx.QueryRow(ctx, `INSERT INTO credentials (id, kind, account, nonce, sealed) VALUES ($1, $2, nullif($3, ''), $4, $5)
			ON CONFLICT (id) DO UPDATE SET kind = $2, account = nullif($3, ''), nonce = $4, sealed = $5, updated_at = now()
			RETURNING id, kind, coalesce(account, ''), created_at, updated_at`,
			c.ID, c.Kind, c.Account, nonce, sealed).Scan(&after.ID, &after.Kind, &after.Account, &after.CreatedAt, &after.UpdatedAt)
		if err != nil {
			return err
		}
		var bv any
		if before != nil {
			bv = before
		}
		return db.RecordChange(ctx, tx, who, "credentials", c.ID, bv, after)
	})
}

// MaxSize bounds one credential. The largest is gcloud's bundle, a few tens of KB.
const MaxSize = 1 << 20

// Get returns the credential stored under id, or ErrNotFound; ErrUnreadable when it is stored but
// sealed with another key file.
func (v *Vault) Get(ctx context.Context, id string) (Credential, error) {
	var c Credential
	var nonce, sealed []byte
	err := v.pool.QueryRow(ctx, `SELECT id, kind, coalesce(account, ''), nonce, sealed, created_at, updated_at
		FROM credentials WHERE id = $1`, id).Scan(&c.ID, &c.Kind, &c.Account, &nonce, &sealed, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	if err != nil {
		return Credential{}, err
	}
	c.Data, err = v.open(id, nonce, sealed)
	if err != nil {
		return Credential{}, err
	}
	return c, nil
}

// List returns what is stored, without the secrets, by id.
func (v *Vault) List(ctx context.Context) ([]Info, error) {
	rows, err := v.pool.Query(ctx, `SELECT id, kind, coalesce(account, ''), created_at, updated_at FROM credentials ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Info, error) {
		var i Info
		err := r.Scan(&i.ID, &i.Kind, &i.Account, &i.CreatedAt, &i.UpdatedAt)
		return i, err
	})
}

// Delete removes the credential stored under id. Removing one that is not there is not an error.
func (v *Vault) Delete(ctx context.Context, id, who string) error {
	return pgx.BeginFunc(ctx, v.pool, func(tx pgx.Tx) error {
		var b Info
		err := tx.QueryRow(ctx, `DELETE FROM credentials WHERE id = $1
			RETURNING id, kind, coalesce(account, ''), created_at, updated_at`, id).Scan(&b.ID, &b.Kind, &b.Account, &b.CreatedAt, &b.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return db.RecordChange(ctx, tx, who, "credentials", id, b, nil)
	})
}

// Identity is the git identity: the author of every commit (12.6).
type Identity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// BundleFile is one file of a credential that is several files, such as gcloud's.
type BundleFile struct {
	Path string `json:"path"` // relative to the home
	Mode string `json:"mode"`
	Data []byte `json:"data"`
}

// MakeBundle encodes files as one credential.
func MakeBundle(files []BundleFile) ([]byte, error) {
	for _, f := range files {
		if err := ValidHomePath(f.Path); err != nil {
			return nil, err
		}
	}
	return json.Marshal(files)
}

// ParseBundle decodes a credential made by MakeBundle, and refuses a path that leaves the home.
func ParseBundle(data []byte) ([]BundleFile, error) {
	var files []BundleFile
	if err := json.Unmarshal(data, &files); err != nil {
		return nil, fmt.Errorf("a bundle of files: %w", err)
	}
	for _, f := range files {
		if err := ValidHomePath(f.Path); err != nil {
			return nil, err
		}
		if _, err := strconv.ParseUint(f.Mode, 8, 32); err != nil {
			return nil, fmt.Errorf("%s: mode %q", f.Path, f.Mode)
		}
	}
	return files, nil
}

// ValidHomePath refuses a path that is not inside a home (homepath.Valid).
func ValidHomePath(p string) error { return homepath.Valid(p) }
