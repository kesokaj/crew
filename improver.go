// Package crew holds the improver's own definition (10.3): not part of the baseline, and a protected
// part of Crew (16.8), so no improvement changes it.
package crew

import _ "embed"

// Improver is improver.md: the improver's agent definition.
//
//go:embed improver.md
var Improver string
