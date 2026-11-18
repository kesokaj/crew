package sandboxes

import "slices"

// ShortLived says whether a sandbox of kind is removed when the server starts, for the tests.
func ShortLived(kind string) bool { return slices.Contains(shortLived, kind) }
