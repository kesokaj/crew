package setup

import (
	"testing"
)

// The fakes below are assembled at run time, so no file in the repository holds a key header
// or a token shape for a scan to find.
var (
	fakeKeyHeader = "-----" + "BEGIN OPENSSH " + "PRIVATE KEY-----"
	fakeKeyFooter = "-----" + "END OPENSSH " + "PRIVATE KEY-----"
	fakeKeyBody   = "b3BlbnNzaC1rZXktdjE" + "AAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW"
	fakeAPIToken  = "sk-" + "ant-" + "api03-Zm9vYmFyYmF6"
)

// The .pub echoed to the settings page is read without following a symlink,
// but a HARD link to the private key is still read (files.ReadRawNoFollow's
// documented limit), so the content itself must look like a public key
// before it is offered for copying.
func TestPublicKeyLine(t *testing.T) {
	for _, ok := range []string{
		"ecdsa-sha2-nistp521 AAAAE2VjZHNhLXNoYTItbmlzdHA1MjE=",
		"ssh-dss AAAAB3NzaC1kc3MAAACBAI",
		"sk-ecdsa-sha2-nistp256@openssh.com AAAAInNrLWVjZHNhLXNoYTItbmlzdHAyNTZAb3BlbnNzaC5jb20= key",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGb8Q7wq1p3E7c9rZk4x2sY6Hk6s user@host",
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQC7",
		"ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTY= me",
		"sk-ssh-ed25519@openssh.com AAAAGnNrLXNzaC1lZDI1NTE5QG9wZW5zc2guY29t yubikey",
	} {
		if got, accepted := publicKeyLine(ok); !accepted || got != ok {
			t.Errorf("should accept %q unchanged, got %q %v", ok, got, accepted)
		}
	}
	for _, bad := range []string{
		"ssh- AAAA",
		"sk- A",
		fakeAPIToken + " foobar",
		"ssh-ed25519x AAAA",
		"ecdsa-sha2-nistp999 AAAA",
		fakeKeyHeader,
		fakeKeyBody,
		"~/.ssh/id_ed25519",
		"ssh-ed25519",
		"ssh-ed25519 ",
		"ssh-ed25519 not*base64",
		"",
	} {
		if _, accepted := publicKeyLine(bad); accepted {
			t.Errorf("should reject %q", bad)
		}
	}
}

// The comment is echoed to every API consumer, so control and formatting
// characters are stripped and its whitespace collapsed server-side — the
// browser is not the only reader.
func TestPublicKeyLineSanitisesTheComment(t *testing.T) {
	body := "AAAAC3NzaC1lZDI1NTE5AAAAIGb8Q7wq1p3E7c9rZk4x2sY6Hk6s"
	for in, want := range map[string]string{
		"ssh-ed25519 " + body + " user\x1b[31mhost":        "ssh-ed25519 " + body + " user[31mhost",
		"ssh-ed25519 " + body + " a\u202eb\u200bc":         "ssh-ed25519 " + body + " abc",
		"ssh-ed25519 " + body + "\tmy\u00a0laptop   key\t": "ssh-ed25519 " + body + " my laptop key",
		"ssh-ed25519 " + body + " my   laptop\u2003key":    "ssh-ed25519 " + body + " my laptop key",
	} {
		got, ok := publicKeyLine(in)
		if !ok {
			t.Errorf("%q should be accepted", in)
			continue
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// The handler, not only the function: a .pub that is a symlink to the
// private key, a hard link to it, or a byte copy of it must leave the ssh
// tool with no Details and not Ready; a real key line is echoed. Below a
