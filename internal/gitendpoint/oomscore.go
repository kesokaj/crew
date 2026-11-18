package gitendpoint

import "strings"

// oomScoreFile is where the endpoint's git sets the score by which the kernel chooses what to stop
// when the server's memory is full; a test names another.
var oomScoreFile = "/proc/self/oom_score_adj"

// GitOOMScore is that score, of the kernel's range of -1000 to 1000 (6.1, 13.10; since 1.08): the
// git of every push, fetch and advertisement of refs, a push's hook and the server's program the hook
// starts, which inherit it, are stopped before the server, whose score stays 0. A process may lower its
// own score again, down to 0, without a privilege: nothing the endpoint starts does, and none of it is
// a program a run brought. A request whose score cannot be set is not taken.
const GitOOMScore = "500"

// backend is the program and the arguments the CGI handler starts: a shell that sets the score and
// then becomes git http-backend.
func (e *Endpoint) backend() (string, []string) {
	file := "'" + strings.ReplaceAll(oomScoreFile, "'", `'\''`) + "'"
	return "/bin/sh", []string{"-c", "printf " + GitOOMScore + " >" + file + ` && exec "$0" http-backend`, e.git()}
}
