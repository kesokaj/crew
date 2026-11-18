// Package version holds the version of this build of Crew.
package version

import "strings"

// Version is set at build time with -ldflags "-X github.com/kesokaj/crew/internal/version.Version=...".
// A build from a checkout at no tag is 0.0.0-dev.<commit> (design 16.11), never a moving tag.
var Version = "0.0.0-dev"

// IsDev reports whether this build is not a release.
func IsDev() bool { return strings.Contains(Version, "-dev") }
