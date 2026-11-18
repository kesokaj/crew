package redact

import (
	"regexp"
	"strings"
)

// The needles are copied from styrman-harness 43faca0, internal/harness/report.go (design 8.3,
// 17.3). A needle locates a secret; it never says how much of it there is: a PEM body carries no
// needle at all, and AKIA is only a prefix. So what goes is always a whole line, or a whole key
// block from its header to its footer.

// prefixNeedles are vendor token shapes, matched with case. Nothing shorter than a vendor's own
// prefix is listed: a bare sk- would match every "task-".
var prefixNeedles = []string{
	"-----BEGIN", "PRIVATE KEY", "AKIA", "ASIA", "AIza", "ya29.", "GOCSPX-",
	"ghp_", "gho_", "ghs_", "ghu_", "ghr_", "github_pat_", "glpat-", "gldt-", "glrt-", "glptt-",
	"sk-ant-", "sk-proj-", "xoxb-", "xoxp-", "xoxa-", "xoxs-",
	// Stripe webhook secrets, and PyPI tokens, which open with the base64 of their macaroon's location.
	"whsec_", "pypi-AgEIcHlwaS5vcmc", "pypi-AgENdGVzdC5weXBpLm9yZw",
	"sk-svcacct-", "sk-admin-", "sk-None-",
	"AGE-SECRET-KEY-", "dop_v1_", "doo_v1_", "dor_v1_",
}

// secretRe is an assignment to a key of the pass family, a secret, a token, or an API key.
var secretRe = regexp.MustCompile(`(?i)(?P<flag>--)?(?P<key>(?:pass(?:word|wd|phrase)?|pwd|secret|token|api[_-]?key)[a-z0-9_-]*)["']?\s*(?P<sep>[:=]|\s)\s*(?:"(?P<dq>[^"]*)"|'(?P<sq>[^']*)'|(?P<bare>\S*))`)

// keywordNeedles are assignment and header shapes, and token shapes known by their length and
// alphabet, matched without case where the forms vary (GITHUB_TOKEN=, Authorization: Bearer).
var keywordNeedles = []struct {
	pattern string
	re      *regexp.Regexp
	// accept judges a match by its parts, for a needle whose shape alone reaches too far: an
	// assignment whose "value" is prose.
	accept func(m []string) bool
}{
	// A quoted value is a secret whatever it holds. An unquoted one is one from four characters when
	// the whole key holds pass, pwd, or secret, and from eight, or six with a digit, for a token or an
	// API key. A heading, a field name, or a word followed by a space is prose.
	{"secret assignment", secretRe, func(m []string) bool {
		g := func(name string) string { return m[secretRe.SubexpIndex(name)] }
		if strings.TrimSpace(g("sep")) == "" && g("flag") == "" {
			return false
		}
		if g("dq") != "" || g("sq") != "" {
			return true
		}
		v := strings.TrimRight(g("bare"), `,;)`)
		key := strings.ToLower(g("key"))
		if strings.Contains(key, "pass") || strings.Contains(key, "pwd") || strings.Contains(key, "secret") {
			return len(v) >= 4
		}
		return len(v) >= 8 || len(v) >= 6 && strings.ContainsAny(v, "0123456789")
	}},
	{"bearer", regexp.MustCompile(`(?i)bearer\s+\S`), nil},
	{"authorization", regexp.MustCompile(`(?i)authorization["']?\s*:\s*\S`), nil},
	// The user may be empty: redis://:pass@host.
	{"url credentials", regexp.MustCompile(`://[^/\s:@]*:[^/\s@]+@`), nil},
	// No word boundary in front: a token after a written-out \n or an underscore sits inside a word.
	{"jwt", regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.`), nil},
	{"stripe key", regexp.MustCompile(`[sr]k_(live|test)_[A-Za-z0-9]{16,}`), nil},
	{"openai key", regexp.MustCompile(`sk-[A-Za-z0-9]{32,}`), nil},
	{"npm token", regexp.MustCompile(`npm_[A-Za-z0-9]{36}`), nil},
	{"hugging face token", regexp.MustCompile(`hf_[A-Za-z0-9]{30,}`), nil},
	// A token as the whole user part of a URL: sixteen characters or more up to the @, so a short
	// user such as ssh://git@ stays.
	{"url token", regexp.MustCompile(`://[^/\s:@]{16,}:?@`), nil},
	{"slack webhook", regexp.MustCompile(`(?i)hooks\.slack`), nil},
	// A JWT's payload may be as short as e30, an empty object.
	{"jwt short", regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*\.[A-Za-z0-9_-]{8,}`), nil},
	{"google refresh token", regexp.MustCompile(`1//0[A-Za-z0-9_-]{20,}`), nil},
	{"discord webhook", regexp.MustCompile(`(?i)discord(app)?\.com[:.]?[0-9]*/api(/v[0-9]+)?/webhooks/`), nil},
	{"teams webhook", regexp.MustCompile(`(?i)webhook\.office\.com/|/webhookb2/|[?&]sig=[A-Za-z0-9%_-]{16,}`), nil},
	{"sendgrid key", regexp.MustCompile(`SG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}`), nil},
	{"azure account key", regexp.MustCompile(`(?i)(accountkey|sharedaccesskey|storage_key|access_key)=\S`), nil},
	// The -b/--cookie flag needs a name=value after it: git checkout -b x is not a cookie.
	{"cookie header", regexp.MustCompile(`(?i)(set-)?cookie["']?\s*:\s*\S|(^|\s)(-b|--cookie)[ =]["']?\S+=\S|document\.cookie\s*=`), nil},
	{"vault token", regexp.MustCompile(`hv[sbr]\.[A-Za-z0-9_-]{20,}`), nil},
	// A credential on a command line: user:password after -u/--user/--username, a password flag
	// after a client that takes one, htpasswd -b, an LDAP tool's -w, OpenSSL's pass:, keytool's
	// -storepass and -keypass, and lftp's -u user,password.
	{"command-line credential", regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-]|\\[nrt])(?:-u|--user|--username)(?:\s+|=|["']\s*,\s*["'])?\\?["']?(?:[A-Za-z0-9_.][^\s:%"']*)?["']?[:%]["']?[^\s"']+|(?:sshpass|mysql\S*|mariadb\S*|redis-cli|docker\s+login|podman\s+login|mongo(?:sh)?|sqlcmd)\b[^\n]*?\s-[pa]\s*\S+|htpasswd\s+-[a-z]*b|ldap\w+\b[^\n]*?\s(?-i:-w)\s*\S+|-pass(?:in|out|word)?\s+["']?pass:\S+|-(?:src|dest)?(?:store|key)pass\s+\S+|lftp\b[^\n]*?\s-u\s*[^\s,]+,\S+`), nil},
	// An auth key holding base64: Docker's "auth", .npmrc's _auth, Kubernetes' .dockerconfigjson.
	{"auth key", regexp.MustCompile(`(?i)"auth"\s*:\s*"[A-Za-z0-9+/=]{8,}"|(?:["']?_?auth["']?|\.?dockerconfigjson["']?)\s*[:=]\s*["']?[A-Za-z0-9+/]{16,}={0,2}`), nil},
	// A credential(s) key whose value is sixteen token characters or more and is not a path.
	{"credentials key", regexp.MustCompile(`(?i)credentials?["']?\s*[:=]\s*["']?[A-Za-z0-9+=_-][A-Za-z0-9+/=_.-]{15,}`), nil},
}

// A key block opens at a PEM header, or a header whose dashes were rewritten, and closes at a
// footer.
var (
	blockOpen  = regexp.MustCompile(`-----BEGIN|BEGIN[A-Z ]*PRIVATE KEY`)
	blockClose = regexp.MustCompile(`-----END[^-]*(-----)?|END[A-Z ]*PRIVATE KEY`)
)

// triggers are, in lower case, a fragment that each needle needs: a line that holds none of them holds
// no needle, and is not run through the needles' patterns, which cost a hundred times more.
var triggers = func() []string {
	t := []string{
		"begin", "private key", // key blocks
		"pass", "pwd", "secret", "token", "api", // secret assignment
		"bearer", "authorization", "://", "eyj", "k_live_", "k_test_", "sk-", "npm_", "hf_", "hooks.slack", "1//0",
		"discord", "webhook", "sig=", "sg.", "accountkey", "sharedaccesskey", "storage_key", "access_key",
		"cookie", "-b", "hvs.", "hvb.", "hvr.",
		"-u", "--user", "sshpass", "mysql", "mariadb", "redis-cli", "docker", "podman", "mongo", "sqlcmd", "htpasswd",
		"ldap", "pass:", "storepass", "keypass", "lftp", // command-line credential
		"auth", "dockerconfigjson", "credential",
	}
	for _, n := range prefixNeedles {
		t = append(t, strings.ToLower(n))
	}
	return t
}()

// mayHold says whether s holds a fragment some needle needs. It may say yes for a line that holds no
// needle; it never says no for one that holds one.
func mayHold(s string) bool {
	l := strings.ToLower(s)
	for _, t := range triggers {
		if strings.Contains(l, t) {
			return true
		}
	}
	return false
}

// needleIn names the first needle s holds.
func needleIn(s string) (string, bool) {
	for _, n := range prefixNeedles {
		if strings.Contains(s, n) {
			return n, true
		}
	}
	for _, k := range keywordNeedles {
		if k.accept == nil {
			if k.re.MatchString(s) {
				return k.pattern, true
			}
			continue
		}
		for _, m := range k.re.FindAllStringSubmatch(s, -1) {
			if k.accept(m) {
				return k.pattern, true
			}
		}
	}
	return "", false
}
