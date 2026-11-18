package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

// Made-up values in the shapes of the secrets Crew hands a sandbox.
const (
	runToken = "3f9a1c0d5e7b2a4c6e8f0a1b3c5d7e9f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d"
	// Split so the repository's own scrub check does not take the made-up token for a real one.
	ghToken = "gh" + "s_abcdefghijklmnopqrstuvwxyz0123456789"
)

func TestTextRemovesValuesAndTheLinesWithANeedle(t *testing.T) {
	r := New(runToken, "short")
	in := "the token is " + runToken + " here\nsafe line\nexport GITHUB_TOKEN=" + "abcdefgh12" + "\nshort stays\n"
	out := r.Text(in)
	if strings.Contains(out, runToken) || strings.Contains(out, "abcdefgh12") {
		t.Fatalf("%q", out)
	}
	if !strings.Contains(out, "the token is [redacted] here\n") || !strings.Contains(out, "safe line\n") ||
		!strings.Contains(out, "short stays") || !strings.Contains(out, "[redacted: secret assignment]\n") {
		t.Fatalf("%q", out)
	}
}

// Needles with none of the marks of an assignment are found too: a vendor's prefix, a bearer, and a
// key block whose dashes were rewritten.
func TestTextFindsNeedlesWithoutPunctuation(t *testing.T) {
	for _, in := range []string{
		"AKIA" + "IOSFODNN7EXAMPLE",
		"AIza" + "SyDaGmWKa4JsXZHjGw7ISLn1234567890ab",
		"curl with Bearer abcdefghijklmnop",
		"BEGIN RSA " + "PRIVATE KEY\nMIIEowIBAAKCAQEA\nEND RSA " + "PRIVATE KEY",
	} {
		if out := New().Text(in); out == in || strings.Contains(out, "IOSFODNN") || strings.Contains(out, "abcdefghijklmnop") ||
			strings.Contains(out, "MIIEow") || strings.Contains(out, "SyDaGm") {
			t.Errorf("%q came through as %q", in, out)
		}
	}
}

func TestTextRemovesAWholeKeyBlock(t *testing.T) {
	in := "before\n-----" + "BEGIN OPENSSH " + "PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\nAAAABG5vbmUAAAAEbm9uZQ\n-----END OPENSSH PRIVATE KEY-----\nafter\n"
	out := New().Text(in)
	if strings.Contains(out, "b3BlbnNz") || strings.Contains(out, "AAAABG5v") || out != "before\n[redacted: a key]\nafter\n" {
		t.Fatalf("%q", out)
	}
}

func TestTextLeavesCleanTextAsItIs(t *testing.T) {
	for _, s := range []string{"", "hello", "git checkout -b feature/x\nls -la\n", "a token: field\n", "passwords are hard"} {
		if out := New(runToken).Text(s); out != s {
			t.Errorf("%q became %q", s, out)
		}
	}
}

func TestLineKeepsAJSONLineParseable(t *testing.T) {
	r := New(runToken, ghToken)
	line := []byte(`{"type":"user","message":{"content":[{"type":"tool_result","content":"export TOKEN=` + ghToken + `\nok\n"}]},"n":1}`)
	out := r.Line(line)
	if !json.Valid(out) || strings.Contains(string(out), ghToken) {
		t.Fatalf("%s", out)
	}
	if !strings.Contains(string(out), `"n":1`) || !strings.Contains(string(out), `ok\n`) {
		t.Fatalf("the rest of the line changed: %s", out)
	}
	clean := []byte(`{"a":"b",  "c":[1, 2]}`)
	if out := r.Line(clean); string(out) != string(clean) {
		t.Fatalf("a clean line changed: %s", out)
	}
}

// A secret behind a JSON escape is found: the strings are decoded before they are checked.
func TestLineFindsAValueBehindAnEscape(t *testing.T) {
	// The escape is built here, so the source holds a backslash and not the letter.
	escaped := strings.ReplaceAll(runToken, "a", `\u0061`)
	out := New(runToken).Line([]byte(`{"x":"` + escaped + `"}`))
	if strings.Contains(string(out), "\\u0061") || !strings.Contains(string(out), Marker) {
		t.Fatalf("%s", out)
	}
}

func TestLineThatIsNotJSONIsText(t *testing.T) {
	out := New(runToken).Line([]byte("AGY_ERROR: " + runToken))
	if string(out) != "AGY_ERROR: [redacted]" {
		t.Fatalf("%s", out)
	}
}

func TestValueWalksEveryString(t *testing.T) {
	v := map[string]any{"a": []any{"x " + runToken, map[string]any{"b": "Authorization: Bearer abc"}}, "n": 3.0}
	out := New(runToken).Value(v).(map[string]any)
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), runToken) || strings.Contains(string(b), "Bearer abc") || out["n"] != 3.0 {
		t.Fatalf("%s", b)
	}
}

func TestBytesKeepsABinaryFileItsSize(t *testing.T) {
	in := []byte("SQLite format 3\x00 ... " + runToken + " ...")
	out := New(runToken).Bytes(in)
	if len(out) != len(in) || strings.Contains(string(out), runToken) {
		t.Fatalf("%q", out)
	}
	jsonl := []byte(`{"a":"` + runToken + `"}` + "\n" + `{"b":1}` + "\n")
	if out := string(New(runToken).Bytes(jsonl)); strings.Contains(out, runToken) || !strings.HasSuffix(out, `{"b":1}`+"\n") {
		t.Fatalf("%q", out)
	}
}

// A file of a conversation's work folder (plan M20 D33): a text through Text, so a value and a line
// with a needle go; anything that is not valid UTF-8 by value only, so the line rules never cut it.
func TestFileScrubsATextAndABinaryByValue(t *testing.T) {
	r := New(runToken)
	text := "notes\nthe token is " + runToken + "\nmore\n"
	if out := string(r.File([]byte(text))); strings.Contains(out, runToken) || !strings.Contains(out, "notes\n") || !strings.Contains(out, Marker) {
		t.Fatalf("text: %q", out)
	}
	bin := append([]byte{0xff, 0xfe, '\n'}, []byte("password=hunter2-long-enough\n"+runToken)...)
	out := r.File(bin)
	if strings.Contains(string(out), runToken) || !strings.Contains(string(out), "password=hunter2-long-enough") || out[0] != 0xff {
		t.Fatalf("binary: %q", out)
	}
}

// The prefilter never lets a needle through: every needle's own sample line, and each prefix, is
// one it says may hold one.
func TestThePrefilterNeverHidesANeedle(t *testing.T) {
	samples := []string{
		`password = "x"`, "TOKEN_X=abcdefgh1", "--api-key 12345678", "Authorization: Basic x", "Bearer abc",
		"https://u:p@h", "eyJhbGciOiJ.eyJzdWIiOiI.sig", "sk_live_" + "abcdefghijklmnop", "sk-" + strings.Repeat("a", 32),
		"npm_" + strings.Repeat("a", 36), "hf_" + strings.Repeat("a", 30), "https://" + strings.Repeat("a", 16) + "@x",
		"https://hooks.slack.com/x", "1//0" + strings.Repeat("a", 20), "discord.com/api/webhooks/1",
		"webhook.office.com/x", "SG." + strings.Repeat("a", 16) + "." + strings.Repeat("a", 16), "AccountKey=x",
		"Cookie: a=b", "hvs." + strings.Repeat("a", 20), "curl -u user:pass", "sshpass -p x", `"auth": "abcdefgh"`,
		"credentials=" + strings.Repeat("a", 16), "BEGIN RSA " + "PRIVATE KEY",
	}
	for _, n := range prefixNeedles {
		samples = append(samples, "x"+n+"x")
	}
	for _, s := range samples {
		if _, ok := needleIn(s); ok && !mayHold(s) {
			t.Errorf("the prefilter hides a needle in %q", s)
		}
	}
	if mayHold("plain words and numbers 12345") {
		t.Error("a plain line is not passed by")
	}
}

func BenchmarkTextOfPlainLines(b *testing.B) {
	text := strings.Repeat("the quick brown fox jumps over the lazy dog 0123456789\n", 1<<14)
	r := New("made-up-secret-value")
	b.SetBytes(int64(len(text)))
	for b.Loop() {
		r.Text(text)
	}
}
