package settings

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

func projectBounds(merged Doc) []string {
	var errs []string
	for _, p := range []string{"limits.run_timeout_minutes", "limits.run_max_turns", "limits.max_goal_rounds",
		"limits.check_timeout_minutes", "limits.max_fix_rounds", "limits.max_ticket_failures", "limits.max_parallel_subtasks"} {
		if v, ok := num(merged, p); ok && v < 1 {
			errs = append(errs, p+": must be 1 or more")
		}
	}
	for _, p := range []string{"limits.max_added_tickets", "limits.max_added_subtasks", "approvals.new_feature_wait_hours"} {
		if v, ok := num(merged, p); ok && v < 0 {
			errs = append(errs, p+": must be 0 or more")
		}
	}
	if v, ok := num(merged, "limits.max_added_share"); ok && (v < 0 || v > 1) {
		errs = append(errs, "limits.max_added_share: must be from 0 to 1")
	}
	for _, b := range []struct {
		path     string
		low, top float64
	}{
		{"limits.max_active_tickets", 1, 32},
		{"sandbox.cpus", 2, 32},
		{"sandbox.memory_gb", 2, 64},
		{"sandbox.disk_gb", 5, 500},
		{"sandbox.suspend_after_minutes", 1, 240},
		{"sandbox.stop_after_minutes", 1, 1440},
		{"sandbox.max_snapshots", 1, 50},
	} {
		if v, ok := num(merged, b.path); ok && (v < b.low || v > b.top) {
			errs = append(errs, fmt.Sprintf("%s: must be from %g to %g", b.path, b.low, b.top))
		}
	}
	if susp, ok1 := num(merged, "sandbox.suspend_after_minutes"); ok1 {
		if stop, ok2 := num(merged, "sandbox.stop_after_minutes"); ok2 && stop < susp {
			errs = append(errs, "sandbox.stop_after_minutes: must be at least sandbox.suspend_after_minutes")
		}
	}
	return append(errs, hostingBounds(merged)...)
}

var (
	hostPath     = regexp.MustCompile(`^/[A-Za-z0-9._~/-]{0,199}$`)
	dataPath     = regexp.MustCompile(`^/[A-Za-z0-9._/-]{1,199}$`)
	dockerfileRe = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	mailAddress  = regexp.MustCompile(`^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$`)
	// The guest's own folders: the data folder is mounted over none of them (plan M19 D31).
	guestFolders = []string{"/proc", "/sys", "/dev", "/etc", "/run", "/var/run", "/bin", "/sbin", "/lib", "/usr"}
)

// hostingBounds bounds a hosted project's settings (19.3). The app runs as no root, so its port is
// 1024 or more (plan M19 D13); the health address and the data folder are plain paths, the data folder
// none of the guest's own and not /tmp, which is a tmpfs; the Dockerfile's name reaches the build's
// script as an argument, and is kept plain anyway (D35).
func hostingBounds(merged Doc) []string {
	var errs []string
	h, _ := merged["hosting"].(Doc)
	if h == nil {
		return nil
	}
	if v, ok := num(merged, "hosting.port"); ok && (v < 1024 || v > 65535) {
		errs = append(errs, "hosting.port: must be from 1024 to 65535; the app runs as no root")
	}
	if v, ok := h["health"].(string); ok && !hostPath.MatchString(v) {
		errs = append(errs, "hosting.health: must be a path such as /healthz, of letters, digits and . _ ~ / -")
	}
	if v, ok := h["data"].(string); ok {
		// A path as Docker will clean it: /./etc is /etc (security review of M19).
		bad := !dataPath.MatchString(v) || path.Clean(v) != v || v == "/tmp" || strings.HasPrefix(v, "/tmp/") || slices.Contains(strings.Split(v, "/"), "..")
		for _, f := range guestFolders {
			bad = bad || v == f || strings.HasPrefix(v, f+"/")
		}
		if bad {
			errs = append(errs, "hosting.data: must be a folder such as /data, not /, /tmp or a folder of the system")
		}
	}
	// The same rule as the build's (harness.DockerfileProblem): no part of the path is empty, . or ..
	if v, ok := h["dockerfile"].(string); ok && (!dockerfileRe.MatchString(v) || slices.ContainsFunc(strings.Split(v, "/"), func(p string) bool {
		return p == "" || p == "." || p == ".."
	})) {
		errs = append(errs, "hosting.dockerfile: must be a path in the repository, of letters, digits and . _ / -")
	}
	for _, b := range []struct {
		key      string
		from, to float64
	}{{"hosting.public_rps", 1, 1000}, {"hosting.log_days", 1, 365}, {"hosting.data_copies", 1, 10}} {
		if v, ok := num(merged, b.key); ok && (v < b.from || v > b.to) {
			errs = append(errs, fmt.Sprintf("%s: must be from %g to %g", b.key, b.from, b.to))
		}
	}
	if list, ok := h["allowed"].([]any); ok {
		for _, e := range list {
			if s, ok := e.(string); !ok || !mailAddress.MatchString(s) {
				errs = append(errs, fmt.Sprintf("hosting.allowed: %v is not a mail address", e))
			}
		}
	}
	return errs
}
