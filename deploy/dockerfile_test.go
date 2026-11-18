package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The server's image is built from the folders its Dockerfile copies: every package of Crew's module
// that the server's binary is built from is in one of them. A folder missed fails only when an
// install builds the image, which make check does not do.
func TestTheServersImageCopiesEveryFolderItsBuildReads(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "list", "-deps", "-f", "{{if .Module}}{{if .Module.Main}}{{.Dir}}{{end}}{{end}}", "./cmd/crew", "./cmd/crew-updater")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	df, err := os.ReadFile("Dockerfile.server")
	if err != nil {
		t.Fatal(err)
	}
	// The build stage's COPY lines, up to the next stage.
	stage := string(df)[strings.Index(string(df), " AS build"):]
	if i := strings.Index(stage[1:], "\nFROM "); i >= 0 {
		stage = stage[:i+1]
	}
	copied := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^COPY ([^ /]+)/ `).FindAllStringSubmatch(stage, -1) {
		copied[m[1]] = true
	}
	// Files copied by name into the root, as the module's root package's are.
	files := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^COPY (.+) \./$`).FindAllStringSubmatch(stage, -1) {
		for _, f := range strings.Fields(m[1]) {
			files[f] = true
		}
	}
	for _, dir := range strings.Fields(string(out)) {
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			continue
		}
		// The module's root package: each of its files and what it embeds is copied by name (the
		// release build of 0.2.0 missed improver.go).
		if rel == "." {
			cmd := exec.Command("go", "list", "-f", `{{join .GoFiles " "}} {{join .EmbedFiles " "}}`, ".")
			cmd.Dir = root
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("go list .: %v", err)
			}
			for _, f := range strings.Fields(string(out)) {
				if !files[f] {
					t.Errorf("the server is built from the root's %s, and deploy/Dockerfile.server does not copy it", f)
				}
			}
			continue
		}
		top := strings.Split(filepath.ToSlash(rel), "/")[0]
		if !copied[top] {
			t.Errorf("the server is built from %s, and deploy/Dockerfile.server does not copy %s/", rel, top)
		}
	}
}

// A build of a release runs in a Sources sandbox, whose proxy takes only CONNECT but for Go's
// modules (since 0.61), so every package source a Dockerfile reads is HTTPS: each stage that runs
// apt-get first takes a CA bundle from a pinned image and moves Debian's sources to
// https://deb.debian.org (the operator's answer of 2026-10-20). apt's packages are signed either
// way; plain HTTP would not leave the sandbox.
func TestEveryAptStageReadsDebianOverHTTPS(t *testing.T) {
	for _, file := range []string{"Dockerfile.server", "../image/Dockerfile", "../ingress/Dockerfile"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		stages := regexp.MustCompile(`(?m)^FROM `).Split(string(body), -1)
		for i, stage := range stages[1:] {
			first := strings.Index(stage, "apt-get")
			if first < 0 {
				continue
			}
			before := stage[:first]
			if !strings.Contains(before, "COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt") ||
				!strings.Contains(before, "s|http://deb.debian.org|https://deb.debian.org|g") ||
				!strings.Contains(before, `Acquire::https::CAInfo "/etc/ssl/certs/ca-certificates.crt";`) {
				t.Errorf("%s, stage %d (%s): apt-get runs before Debian's sources are HTTPS", file, i+1, strings.SplitN(stage, "\n", 2)[0])
			}
		}
		if regexp.MustCompile(`(?m)^[^#]*http://`).MatchString(strings.NewReplacer("s|http://deb.debian.org|", "", "grep -q '^URIs:.*http://'", "").Replace(string(body))) {
			t.Errorf("%s reads a source over plain HTTP", file)
		}
	}
}

// A Sources sandbox gets Go's modules from the server's module proxy, as GOPROXY says (13.6, since
// 0.61), and release.sh hands it to each build as a build argument: each stage of a Dockerfile it
// builds that runs Go's command, or xcaddy, which runs it, declares ARG GOPROXY before it does, so
// the build takes it. Where none is given it is empty, and Go's command takes its default.
func TestEveryGoStageTakesTheModuleProxy(t *testing.T) {
	goRuns := regexp.MustCompile(`\b(go (mod|build|install|run|get|generate|list|test|vet)|xcaddy) `)
	for _, file := range []string{"Dockerfile.server", "../image/Dockerfile", "../ingress/Dockerfile", "../tools/check-image/Dockerfile"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		code := regexp.MustCompile(`(?m)^\s*#.*$`).ReplaceAllString(string(body), "")
		found := 0
		for i, stage := range regexp.MustCompile(`(?m)^FROM `).Split(code, -1)[1:] {
			first := goRuns.FindStringIndex(stage)
			if first == nil {
				continue
			}
			found++
			if !regexp.MustCompile(`(?m)^ARG GOPROXY$`).MatchString(stage[:first[0]]) {
				t.Errorf("%s, stage %d (%s): Go's command runs before ARG GOPROXY", file, i+1, strings.SplitN(stage, "\n", 2)[0])
			}
		}
		if found == 0 {
			t.Errorf("%s: no stage runs Go's command, so this test reads it wrong", file)
		}
	}
}

// make check hands the module proxy to the containers that run Go's command, as it hands them the
// proxy of a Sources sandbox (13.6, since 0.61).
func TestMakeCheckHandsGoTheModuleProxy(t *testing.T) {
	cmd := exec.Command("make", "-n", "-C", "..", "test-go")
	cmd.Env = append(withoutProxy(os.Environ()), "GOPROXY=http://10.9.1.254:8091/goproxy")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "-e 'GOPROXY=http://10.9.1.254:8091/goproxy'") {
		t.Fatalf("the Go container is not given the module proxy:\n%s", out)
	}
	cmd = exec.Command("make", "-n", "-C", "..", "test-go")
	cmd.Env = withoutProxy(os.Environ())
	if out, err = cmd.CombinedOutput(); err != nil || strings.Contains(string(out), "GOPROXY") {
		t.Fatalf("with none set: %v\n%s", err, out)
	}
}
