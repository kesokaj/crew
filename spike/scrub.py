#!/usr/bin/env python3
"""Scrub secrets out of spike recordings, and check files before a commit.

This script holds no secret. The values to remove are read from a file that
git ignores, spike/raw/scrub-values.txt, which the operator fills in: one
value on each line (tokens, account ids, mail addresses). Blank lines and
lines that start with "#" are skipped.

Besides those values it removes, and the check refuses, whatever has the
shape of a token (TOKEN_SHAPES below). Mail addresses are removed by scrub,
but not refused by check, because the plan documents name example addresses.

  scrub.py scrub IN [OUT]      write IN scrubbed to OUT, or to standard output
  scrub.py check FILE...       fail when a file holds a value or a token shape
  scrub.py check --staged      the same, over what is staged in git

check exits 0 when clean, 1 when something was found, and 2 when it could
not run (no values file, or a value too short to scrub safely). It names the
file, the line, and the kind of finding, and never prints what it found.
"""

import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_VALUES = os.path.join(HERE, "raw", "scrub-values.txt")
MIN_VALUE_LEN = 6

# Each shape is a kind and a pattern. A match is replaced by "[<kind>]".
TOKEN_SHAPES = [
    ("google-access-token", r"ya29\.[0-9A-Za-z_\-]{20,}"),
    ("google-refresh-token", r"1//0[0-9A-Za-z_\-]{20,}"),
    ("google-api-key", r"AIza[0-9A-Za-z_\-]{35}"),
    ("google-client-secret", r"GOCSPX-[0-9A-Za-z_\-]{20,}"),
    ("anthropic-token", r"sk-ant-[a-z]{2,4}[0-9]{2}-[0-9A-Za-z_\-]{20,}"),
    ("github-token", r"gh[pousr]_[0-9A-Za-z]{30,}"),
    ("github-pat", r"github_pat_[0-9A-Za-z_]{40,}"),
    ("slack-token", r"xox[abprs]-[0-9A-Za-z\-]{10,}"),
    ("aws-key-id", r"AKIA[0-9A-Z]{16}"),
    ("jwt", r"eyJ[0-9A-Za-z_\-]{10,}\.eyJ[0-9A-Za-z_\-]{10,}\.[0-9A-Za-z_\-]{10,}"),
    ("private-key", r"-----BEGIN [A-Z ]*PRIVATE KEY-----"),
    # A JSON field whose name says it is secret, with a value of some length.
    # Only the middle group is replaced, so the JSON stays valid.
    ("secret-field",
     r'("(?:access_token|refresh_token|id_token|client_secret|oauth_token'
     r'|api_key|apiKey|accessToken|refreshToken|idToken|clientSecret)":\s?")'
     r'([^"]{8,})(")'),
]
EMAIL = re.compile(r"[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}")
# Kept: example addresses, and the fixed no-reply addresses of the vendors,
# which a Co-Authored-By trailer names (design 12.2).
EMAIL_KEEP = re.compile(r"(@(example\.(com|org|net)|users\.noreply\.github\.com)|^noreply@anthropic\.com)$",
                        re.IGNORECASE)

SHAPES = [(kind, re.compile(p)) for kind, p in TOKEN_SHAPES]


def load_values(path):
    """Return the values, longest first, so a longer value wins over its part."""
    if not os.path.isfile(path):
        sys.exit(f"scrub: no values file at {path}: the operator fills it in (spike/README.md)")
    values = []
    with open(path, encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            v = line.strip()
            if not v or v.startswith("#"):
                continue
            if len(v) < MIN_VALUE_LEN:
                sys.exit(f"scrub: line {n} of the values file is shorter than "
                         f"{MIN_VALUE_LEN} characters, too short to scrub safely")
            values.append(v)
    return sorted(set(values), key=len, reverse=True)


def value_pattern(v):
    # Mail addresses differ in case; tokens and ids do not.
    flags = re.IGNORECASE if "@" in v else 0
    return re.compile(re.escape(v), flags)


def scrub_text(text, values):
    counts = {}

    def sub(pattern, repl, kind, s):
        s, n = pattern.subn(repl, s)
        if n:
            counts[kind] = counts.get(kind, 0) + n
        return s

    for i, v in enumerate(values, 1):
        text = sub(value_pattern(v), f"[scrubbed-{i}]", "value", text)
    for kind, pattern in SHAPES:
        if pattern.groups == 3:
            repl = lambda m, k=kind: m.group(1) + f"[{k}]" + m.group(3)
        else:
            repl = f"[{kind}]"
        text = sub(pattern, repl, kind, text)
    text = sub(EMAIL, lambda m: m.group(0) if EMAIL_KEEP.search(m.group(0)) else "[email]",
               "email", text)
    if "email" in counts:
        # subn counts kept addresses too; count only the replaced ones.
        counts["email"] = text.count("[email]")
        if not counts["email"]:
            del counts["email"]
    return text, counts


def findings(text, values):
    """Yield (line number, kind) for every value or token shape in text."""
    patterns = [("value", value_pattern(v)) for v in values] + SHAPES
    for n, line in enumerate(text.splitlines(), 1):
        for kind, pattern in patterns:
            if pattern.search(line):
                yield n, kind


def staged_files():
    out = subprocess.run(
        ["git", "diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z"],
        check=True, capture_output=True).stdout
    for name in out.split(b"\0"):
        if name:
            blob = subprocess.run(["git", "show", b":" + name], check=True,
                                  capture_output=True).stdout
            yield name.decode(), blob


def read_files(names):
    for name in names:
        with open(name, "rb") as f:
            yield name, f.read()


def cmd_check(args, values):
    files = staged_files() if args == ["--staged"] else read_files(args)
    bad = 0
    seen = 0
    for name, data in files:
        seen += 1
        text = data.decode("utf-8", errors="replace")
        for n, kind in findings(text, values):
            print(f"scrub check: {name}:{n}: {kind}")
            bad += 1
    if bad:
        print(f"scrub check: FAIL, {bad} finding(s) in {seen} file(s)")
        return 1
    print(f"scrub check: ok, {seen} file(s), {len(values)} value(s) and "
          f"{len(SHAPES)} token shapes")
    return 0


def cmd_scrub(args, values):
    if len(args) not in (1, 2):
        sys.exit("usage: scrub.py scrub IN [OUT]")
    with open(args[0], encoding="utf-8", errors="surrogateescape") as f:
        text, counts = scrub_text(f.read(), values)
    if len(args) == 2:
        with open(args[1], "w", encoding="utf-8", errors="surrogateescape") as f:
            f.write(text)
    else:
        sys.stdout.write(text)
    summary = ", ".join(f"{k} {n}" for k, n in sorted(counts.items())) or "nothing"
    print(f"scrub: {args[0]}: replaced {summary}", file=sys.stderr)
    # The scrubbed text must pass the check, or the shapes and the scrub disagree.
    left = list(findings(text, values))
    if left:
        print(f"scrub: {len(left)} finding(s) left after scrubbing", file=sys.stderr)
        return 1
    return 0


def main(argv):
    values_path = os.environ.get("SCRUB_VALUES", DEFAULT_VALUES)
    if len(argv) < 2 or argv[0] not in ("scrub", "check"):
        sys.exit(__doc__)
    try:
        values = load_values(values_path)
    except SystemExit as e:
        print(e, file=sys.stderr)
        return 2
    if argv[0] == "check":
        return cmd_check(argv[1:], values)
    return cmd_scrub(argv[1:], values)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
