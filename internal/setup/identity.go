package setup

import (
	"regexp"
	"strings"
	"unicode"
)

// gitUserFromConfig returns user.name and user.email from gitconfig text,
// with `git config --file`'s reading of the parts that matter here: section
// headers and keys are case-insensitive; `[user]` only — a subsection
// (`[user "x"]`) is a different section; `=` may carry any whitespace;
// a value may be double-quoted, with \" and \\ unescaped; `#` and `;`
// start a comment outside quotes; the LAST assignment wins, across a
// reopened [user] section. Not honoured: [include] directives (git itself
// does not follow them for --file without --includes).
func gitUserFromConfig(text string) (name, email string) {
	inUser := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			// `[user] ; comment` is valid: the header ends at the first `]`.
			hdr, _, ok := strings.Cut(line[1:], "]")
			inUser = ok && strings.EqualFold(strings.TrimSpace(hdr), "user")
			continue
		}
		if !inUser {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = gitConfigValue(v)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "name":
			name = v
		case "email":
			email = v
		}
	}
	// No control characters in an identity: the values reach `git -c` argv
	// in the guest and the broker's TSV env, where a decoded "\n" escape
	// would be a record separator.
	return stripControls(name), stripControls(email)
}

// gitConfigValue unquotes one value: a leading double quote starts a quoted
// string in which \" \\ \n \t \b are git's escapes (any other escaped
// character stands for itself); otherwise the value runs to the first
// unquoted # or ; and is trimmed.
func gitConfigValue(v string) string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "\"") {
		if i := strings.IndexAny(v, "#;"); i >= 0 {
			v = v[:i]
		}
		return strings.TrimSpace(v)
	}
	var out strings.Builder
	esc := false
	for _, r := range v[1:] {
		switch {
		case esc:
			switch r {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case 'b':
				out.WriteByte('\b')
			default:
				out.WriteRune(r)
			}
			esc = false
		case r == '\\':
			esc = true
		case r == '"':
			return out.String()
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// stripControls drops the characters that let untrusted text lie about itself.
// Two whole Unicode categories rather than a hand-listed set: Cc (C0, DEL and
// the C1 block) via unicode.IsControl, and Cf — every invisible formatting
// character, which is what the bidi overrides and isolates (U+202A-U+202E,
// U+2066-U+2069), the zero-width marks (U+200B-U+200F) and the BOM all are.
// The enumerated version of this shipped missing U+061C and drifts by
// construction; categories cannot. U+2028/U+2029 are Zl/Zp rather than Cf, so
// they are named explicitly — they are line breaks, and this text is rendered
// as a single line.
func stripControls(t string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			return -1
		}
		return r
	}, t)
}

// publicKeyLine reports whether line has the shape OpenSSH writes to a .pub —
// a known key type, a space, a base64 body, optionally a comment, on one
// line — and returns it SANITISED: the comment stripped of control and
// formatting characters and its whitespace collapsed to single ASCII spaces,
// since the line is shown to the operator. The type must be one of the tokens
// OpenSSH itself writes: a bare prefix (`ssh- AAAA`, an `sk-ant-…` API token)
// is not a key.
func publicKeyLine(line string) (string, bool) {
	line = strings.ReplaceAll(line, "\t", " ") // a TAB separator is legal in a .pub
	typ, rest, ok := strings.Cut(line, " ")
	if !ok || !keyTypeRe.MatchString(typ) {
		return "", false
	}
	body, comment, _ := strings.Cut(strings.TrimLeft(rest, " "), " ")
	if body == "" {
		return "", false
	}
	for _, r := range body {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/' || r == '=') {
			return "", false
		}
	}
	out := typ + " " + body
	if c := strings.Join(strings.Fields(stripControls(comment)), " "); c != "" {
		out += " " + c
	}
	return out, true
}

// keyTypeRe is the set of key types that produce a .pub OpenSSH can load.
var keyTypeRe = regexp.MustCompile(`^(ssh-(ed25519|rsa|dss)|ecdsa-sha2-nistp(256|384|521)|sk-(ssh-ed25519|ecdsa-sha2-nistp256)@openssh\.com)$`)
