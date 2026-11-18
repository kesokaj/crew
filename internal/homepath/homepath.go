// Package homepath checks a path that is written into a sandbox's home: relative to the home, clean,
// and with no way out of it. The server and crew-run apply the same rule, so it is one function.
package homepath

import (
	"fmt"
	"strings"
)

// Valid refuses a path that is not inside a home: empty, absolute, starting with "~", or with an
// empty, "." or ".." part.
func Valid(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
		return fmt.Errorf("path %q: relative to the home", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "" || seg == "." {
			return fmt.Errorf("path %q: leaves the home or is not clean", p)
		}
	}
	return nil
}
