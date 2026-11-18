package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVerbs(t *testing.T) {
	for _, c := range []struct {
		args []string
		code int
		out  string
	}{
		{[]string{"version"}, 0, "0.0.0-dev"},
		{[]string{"turn"}, 2, ""},
		{[]string{"turn", "../x"}, 2, ""},
		{[]string{"login-check"}, 2, ""},
		{[]string{"login-check", "codex"}, 2, ""},
		{nil, 2, ""},
		{[]string{"nonsense"}, 2, ""},
	} {
		var out, errb bytes.Buffer
		if got := run(c.args, &out, &errb); got != c.code || !strings.Contains(out.String(), c.out) {
			t.Errorf("%v: exit %d, %q; want %d, %q", c.args, got, out.String(), c.code, c.out)
		}
	}
}
