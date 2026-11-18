package cli

import (
	"os"
	"strings"
	"testing"
)

// The pin of agy in versions.lock is an address of the shape the daily look takes, naming its version
// (12.8, since 0.56): the adapter's pattern and check-versions.py's hold the same.
func TestAgysPinIsTheTarballsShape(t *testing.T) {
	body, err := os.ReadFile("../../versions.lock")
	if err != nil {
		t.Fatal(err)
	}
	lock := map[string]string{}
	for _, l := range strings.Split(string(body), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && !strings.HasPrefix(k, "#") {
			lock[k] = v
		}
	}
	m := AgyTarball.FindStringSubmatch(lock["AGY_URL"])
	if m == nil || m[1] != lock["AGY_VERSION"] {
		t.Fatalf("AGY_URL %q, AGY_VERSION %q", lock["AGY_URL"], lock["AGY_VERSION"])
	}
	for _, bad := range []string{
		strings.Replace(lock["AGY_URL"], "storage.googleapis.com", "storage.googleapis.com.example.com", 1),
		strings.Replace(lock["AGY_URL"], "antigravity-public", "antigravity-public2", 1),
		lock["AGY_URL"] + "?x=1",
		"http" + strings.TrimPrefix(lock["AGY_URL"], "https"),
	} {
		if AgyTarball.MatchString(bad) {
			t.Errorf("%q is taken", bad)
		}
	}
	if !strings.HasPrefix(AgyManifest, "https://") {
		t.Fatalf("the manifest %q", AgyManifest)
	}
}

// A build takes agy's tarball as versions.lock pins it, from the vendor's address alone (12.8, since
// 0.57): the address with the pinned version in it, and its sha512. Anything else gives no address,
// and the reason names no value of the lock, which a commit of Crew's project wrote.
func TestAgysDownloadIsThePinAlone(t *testing.T) {
	url := "https://storage.googleapis.com/antigravity-public/antigravity-cli/1.4.5-4952523841339392/linux-x64/cli_linux_x64.tar.gz"
	sum := strings.Repeat("ab", 64)
	if u, s, err := AgyDownload(map[string]string{"AGY_VERSION": "1.4.5", "AGY_URL": url, "AGY_SHA512": sum}); err != nil || u != url || s != sum {
		t.Fatalf("%q %q %v", u, s, err)
	}
	if !strings.HasSuffix(url, "/"+AgyFile) {
		t.Fatalf("the file %q", AgyFile)
	}
	for name, lock := range map[string]map[string]string{
		"another version":   {"AGY_VERSION": "1.4.6", "AGY_URL": url, "AGY_SHA512": sum},
		"another host":      {"AGY_VERSION": "1.4.5", "AGY_URL": strings.Replace(url, "storage.googleapis.com", "evil.example", 1), "AGY_SHA512": sum},
		"another bucket":    {"AGY_VERSION": "1.4.5", "AGY_URL": strings.Replace(url, "antigravity-public", "evil-bucket", 1), "AGY_SHA512": sum},
		"http":              {"AGY_VERSION": "1.4.5", "AGY_URL": "http" + strings.TrimPrefix(url, "https"), "AGY_SHA512": sum},
		"no address":        {"AGY_VERSION": "1.4.5", "AGY_SHA512": sum},
		"no version":        {"AGY_URL": url, "AGY_SHA512": sum},
		"no checksum":       {"AGY_VERSION": "1.4.5", "AGY_URL": url},
		"a short checksum":  {"AGY_VERSION": "1.4.5", "AGY_URL": url, "AGY_SHA512": sum[:127]},
		"an upper checksum": {"AGY_VERSION": "1.4.5", "AGY_URL": url, "AGY_SHA512": strings.ToUpper(sum)},
		"a checksum and $":  {"AGY_VERSION": "1.4.5", "AGY_URL": url, "AGY_SHA512": sum + "$(id)"},
	} {
		u, _, err := AgyDownload(lock)
		if err == nil || u != "" {
			t.Errorf("%s was taken: %q", name, u)
			continue
		}
		if msg := err.Error(); strings.Contains(msg, "evil") || strings.Contains(msg, "1.4.6") || strings.Contains(msg, "$(id)") {
			t.Errorf("%s: the reason holds the lock's value: %s", name, msg)
		}
	}
}
