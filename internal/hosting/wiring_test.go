package hosting

import "github.com/kesokaj/crew/internal/gitstore"

// The server's repository store is what hosting reads main with (BUG-003): a fake of Git answers only
// the methods the real store has.
var _ Git = (*gitstore.Store)(nil)
