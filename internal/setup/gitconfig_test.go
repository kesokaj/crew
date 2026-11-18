package setup

import "testing"

// gitUserFromConfig feeds the commit author and the credential harvest, so a
// stricter reading than git's would silently drop a user's identity from
// every commit. These are the shapes `git config --file` accepts for the
// two keys.
func TestGitUserFromConfig(t *testing.T) {
	for _, c := range []struct{ in, name, email string }{
		{"[user]\n\tname = Ada Lovelace\n\temail = ada@example.com\n", "Ada Lovelace", "ada@example.com"},
		{"[USER]\nName=Ada\nEMAIL=a@b\n", "Ada", "a@b"},                                     // case-insensitive, no spaces
		{"[user]\n  name   =   Ada  \n", "Ada", ""},                                         // whitespace around =
		{"[user]\nname = \"Ada \\\"the\\\" Countess\"\n", "Ada \"the\" Countess", ""},       // quoted with escapes
		{"[user]\nname = Ada # not part\nemail = a@b ; nor this\n", "Ada", "a@b"},           // comments
		{"[user]\nname = First\n[core]\n\tname = wrong\n[user]\nname = Last\n", "Last", ""}, // last wins, reopened, other sections skipped
		{"[user \"sub\"]\nname = Sub\n[user]\nemail = e@x\n", "", "e@x"},                    // a subsection is not [user]
		{"[user] ; trailing comment\nname = Ada\n", "Ada", ""},                              // header with a comment (review nit)
		{"[user]\nname = \"a\\nb\\tc\\qd\"\n", "abcqd", ""},                                 // escapes decode, then control chars are stripped
		{"", "", ""},
		{"garbage without sections\nname = nope\n", "", ""},
	} {
		name, email := gitUserFromConfig(c.in)
		if name != c.name || email != c.email {
			t.Errorf("%q: got (%q, %q), want (%q, %q)", c.in, name, email, c.name, c.email)
		}
	}
}

// stripControls cleans the identity and the key comment. Only the control
// BYTES go; the printable tail of an escape sequence is ordinary text and
// stays visible, which is the point.
func TestStripControls(t *testing.T) {
	strip := map[string]string{
		"a\x1b[2Jb\x07c":      "a[2Jbc",
		"a\u0085b":            "ab",  // C1 (NEL)
		"a\u202eb":            "ab",  // right-to-left override
		"a\u2066b\u2069c":     "abc", // bidi isolates
		"a\u200bb\ufeffc":     "abc", // zero-width + BOM
		"a\u061cb":            "ab",  // arabic letter mark, which a hand-listed
		"a\u2028b\u2029c":     "abc", // set of characters missed, like these two
		"plain ascii is kept": "plain ascii is kept",
		"åäö are kept":        "åäö are kept",
	}
	for in, want := range strip {
		if got := stripControls(in); got != want {
			t.Errorf("stripControls(%q) = %q, want %q", in, got, want)
		}
	}
}
