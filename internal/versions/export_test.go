package versions

// SetMaxChanges sets how many tickets and outside commits Changes lists, for a test, and gives back
// a function that sets it back.
func SetMaxChanges(n int) func() {
	before := maxChanges
	maxChanges = n
	return func() { maxChanges = before }
}

// SetMaxMerges sets how many merges no ticket made Changes reads, for a test.
func SetMaxMerges(n int) func() {
	before := maxMerges
	maxMerges = n
	return func() { maxMerges = before }
}
