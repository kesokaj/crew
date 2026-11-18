#!/usr/bin/env python3
"""Tests of scrub.py. Run: python3 -I spike/scrub_test.py

Every fake token is put together at run time, so that this file itself holds
no token shape and passes the check.
"""

import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRUB = os.path.join(HERE, "scrub.py")

FILL = "Qx7" * 15  # 45 characters that look random enough for every shape
FAKES = {
    "google-access-token": "ya29" + "." + FILL,
    "google-refresh-token": "1//" + "0" + FILL,
    "google-api-key": "AI" + "za" + FILL[:35],
    "google-client-secret": "GOCS" + "PX-" + FILL,
    "anthropic-token": "sk-" + "ant-" + "oat01-" + FILL,
    "github-token": "gh" + "p_" + FILL,
    "github-pat": "github" + "_pat_" + FILL,
    "jwt": "ey" + "J" + FILL[:12] + ".ey" + "J" + FILL[:12] + "." + FILL[:12],
    "private-key": "-----BEGIN " + "RSA PRIVATE" + " KEY-----",
}
VALUE_ID = "acct-" + "5f2e9a1c-0000-4000-8000-1234567890ab"
VALUE_MAIL = "operator.person" + "@" + "mail-of-operator.test"


class ScrubTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.values = os.path.join(self.dir, "values.txt")
        with open(self.values, "w") as f:
            f.write(f"# a comment\n\n{VALUE_ID}\n{VALUE_MAIL}\n")
        self.env = dict(os.environ, SCRUB_VALUES=self.values)

    def run_scrub(self, *args):
        return subprocess.run([sys.executable, "-I", SCRUB, *args], env=self.env,
                              capture_output=True, text=True)

    def write(self, name, text):
        path = os.path.join(self.dir, name)
        with open(path, "w") as f:
            f.write(text)
        return path

    def test_clean_file_passes(self):
        path = self.write("clean.json", '{"type":"init","session_id":"abc","to":"you@example.com"}\n')
        r = self.run_scrub("check", path)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)

    def test_each_token_shape_fails_the_check(self):
        for kind, token in FAKES.items():
            with self.subTest(kind=kind):
                path = self.write("t.txt", f"before\nline with {token} in it\n")
                r = self.run_scrub("check", path)
                self.assertEqual(r.returncode, 1, kind)
                self.assertIn(f":2: {kind}", r.stdout)
                self.assertNotIn(token, r.stdout + r.stderr)

    def test_values_fail_the_check_and_mail_ignores_case(self):
        for value in (VALUE_ID, VALUE_MAIL.upper()):
            with self.subTest(value=value[:5]):
                path = self.write("v.txt", f'{{"account":"{value}"}}\n')
                r = self.run_scrub("check", path)
                self.assertEqual(r.returncode, 1)
                self.assertIn(":1: value", r.stdout)
                self.assertNotIn(value, r.stdout + r.stderr)

    def test_secret_field_fails_the_check(self):
        field = '"refresh' + '_token": "' + 'not-a-known-shape-but-secret' + '"'
        path = self.write("f.json", "{" + field + "}\n")
        self.assertEqual(self.run_scrub("check", path).returncode, 1)

    def test_scrub_removes_everything_and_its_output_passes(self):
        text = "\n".join([f"{k}: {v}" for k, v in FAKES.items()] +
                         [VALUE_ID, VALUE_MAIL, "someone" + "@" + "elsewhere.test",
                          "kept@example.com", "Co-Authored-By: Claude <noreply@anthropic.com>",
                          '{"session_id":"keep-me"}']) + "\n"
        src = self.write("rec.jsonl", text)
        out = os.path.join(self.dir, "out.jsonl")
        r = self.run_scrub("scrub", src, out)
        self.assertEqual(r.returncode, 0, r.stderr)
        with open(out) as f:
            got = f.read()
        for token in list(FAKES.values()) + [VALUE_ID, VALUE_MAIL, "someone@"]:
            self.assertNotIn(token, got)
        self.assertIn("kept@example.com", got)
        self.assertIn("<noreply@anthropic.com>", got)
        self.assertIn('"session_id":"keep-me"', got)
        self.assertEqual(self.run_scrub("check", out).returncode, 0)

    def test_missing_values_file_cannot_run(self):
        self.env["SCRUB_VALUES"] = os.path.join(self.dir, "absent.txt")
        path = self.write("clean.txt", "nothing\n")
        self.assertEqual(self.run_scrub("check", path).returncode, 2)

    def test_short_value_cannot_run(self):
        with open(self.values, "a") as f:
            f.write("abc\n")
        path = self.write("clean.txt", "nothing\n")
        self.assertEqual(self.run_scrub("check", path).returncode, 2)


if __name__ == "__main__":
    unittest.main()
