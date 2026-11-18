// Package hosting hosts a project on the machine (design 19): it builds the project's image in a
// sandbox with no agent, starts it as a microVM of its own, checks it, and switches the ingress's
// route to it, and keeps its deploys, its data, its health, and its logs.
package hosting

import (
	"regexp"
	"strings"
)

// AppsDomainProblem says why apps, the install file's CREW_APPS_DOMAIN, cannot hold hosted projects
// beside Crew's own address crewHost, or "" when it can. That the two are different registered
// domains is crew-ctl's check at install (R172); this one refuses what is plainly the same name or
// one under the other, in case the install file was edited by hand after (plan M19 D23).
func AppsDomainProblem(apps, crewHost string) string {
	a := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(apps)), ".")
	c := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(crewHost)), ".")
	switch {
	case a == "":
		return "Hosting needs an apps domain of its own in the install file (CREW_APPS_DOMAIN)"
	case a == c, strings.HasSuffix(c, "."+a), strings.HasSuffix(a, "."+c):
		return "The apps domain " + a + " is Crew's own address or holds it: hosted projects need another registered domain (CREW_APPS_DOMAIN, 19.4)"
	}
	return ""
}

var label = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// NameProblem says why a project of this name cannot be hosted, or "" when it can: its address is
// <name>.<apps domain> as the name is (plan M19 D10), so the name is a DNS label, and neither of the
// ingress's own names.
func NameProblem(name string) string {
	switch {
	case !label.MatchString(name):
		return "A hosted project's address is its name, so the name must be letters, digits and dashes, ending in a letter or digit"
	case name == "crew", name == "auth":
		return "The name " + name + " is the ingress's own: a project named so cannot be hosted"
	}
	return ""
}
