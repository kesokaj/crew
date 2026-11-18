#!/usr/bin/env python3
"""Tests of the repository's own checks. Run: python3 -I tools/tools_test.py"""
import importlib.util
import pathlib
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace('-', '_'), HERE / f'{name}.py')
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


versions = load('check-versions')
clean = load('check-clean')
HASH = 'a' * 64


class VersionsTest(unittest.TestCase):
    def repo(self, lock, files=None):
        d = pathlib.Path(tempfile.mkdtemp())
        (d / 'versions.lock').write_text(lock)
        for rel, text in (files or {}).items():
            (d / rel).parent.mkdir(parents=True, exist_ok=True)
            (d / rel).write_text(text)
        return d

    def run_check(self, lock, files=None):
        import contextlib
        import io
        with contextlib.redirect_stdout(io.StringIO()) as out:
            code = versions.main(self.repo(lock, files))
        return code, out.getvalue()

    def test_good(self):
        code, out = self.run_check(f'# c\nGO_VERSION=1.25.13\nGO_IMAGE=golang:1.25.13@sha256:{HASH}\n',
                                   {'go.mod': 'module x\n\ngo 1.25.13\n'})
        self.assertEqual(code, 0, out)

    def test_image_without_hash(self):
        code, out = self.run_check('GO_IMAGE=golang:1.25.13\n')
        self.assertEqual(code, 1)
        self.assertIn('sha256 hash', out)

    def test_moving_tag(self):
        code, out = self.run_check(f'X_IMAGE=foo:latest@sha256:{HASH}\nCLAUDE_CODE_VERSION=latest\n')
        self.assertEqual(code, 1)
        self.assertEqual(out.count('moving tag'), 2)

    def test_empty_and_duplicate(self):
        code, out = self.run_check('A=\nB=1\nB=2\nnot a line\n')
        self.assertEqual(code, 1)
        for want in ('A has no value', 'B is given twice', 'not a KEY=VALUE'):
            self.assertIn(want, out)

    def test_place_differs(self):
        code, out = self.run_check('GO_VERSION=1.25.13\n', {'go.mod': 'module x\n\ngo 1.25.0\n'})
        self.assertEqual(code, 1)
        self.assertIn('names 1.25.0 for GO_VERSION', out)

    def test_place_without_key(self):
        code, out = self.run_check('A=1\n', {'web/.node-version': '22.1.0\n'})
        self.assertEqual(code, 1)
        self.assertIn('has no WEB_NODE_VERSION', out)

    def test_dockerfile_arg_differs(self):
        code, out = self.run_check(f'GO_IMAGE=golang:1@sha256:{HASH}\n',
                                   {'deploy/Dockerfile.server': f'ARG GO_IMAGE=golang:2@sha256:{HASH}\nFROM x\n'})
        self.assertEqual(code, 1)
        self.assertIn('ARG GO_IMAGE=golang:2', out)

    def test_dockerfile_drops_a_pin(self):
        code, out = self.run_check(f'CADDY_IMAGE=caddy:1@sha256:{HASH}\n',
                                   {'ingress/Dockerfile': f'ARG CADDY_IMAGE=caddy:1@sha256:{HASH}\n'})
        self.assertEqual(code, 1)
        self.assertIn('names no ARG CADDY_BUILDER_IMAGE', out)

    def test_place_missing_is_fine(self):
        code, out = self.run_check('GO_VERSION=1.25.13\n')
        self.assertEqual(code, 0, out)

    def test_sha512_shape(self):
        code, out = self.run_check(f'A_SHA512={"b" * 128}\nB_SHA512={HASH}\n')
        self.assertEqual(code, 1)
        self.assertIn('B_SHA512 is not a sha512 checksum', out)
        self.assertNotIn('A_SHA512', out)

    # agy is pinned by the vendor's tarball (12.8, since 0.56): its address names its version.
    AGY = 'https://storage.googleapis.com/antigravity-public/antigravity-cli/{}-4952523841339392/linux-x64/cli_linux_x64.tar.gz'

    def test_agy_url_names_its_version(self):
        code, out = self.run_check(f'AGY_VERSION=1.4.5\nAGY_URL={self.AGY.format("1.4.5")}\nAGY_SHA512={"c" * 128}\n')
        self.assertEqual(code, 0, out)
        code, out = self.run_check(f'AGY_VERSION=1.4.5\nAGY_URL={self.AGY.format("1.4.4")}\nAGY_SHA512={"c" * 128}\n')
        self.assertEqual(code, 1)
        self.assertIn('AGY_URL names agy 1.4.4, AGY_VERSION is 1.4.5', out)

    def test_agy_url_of_another_host(self):
        url = self.AGY.format('1.4.5').replace('storage.googleapis.com', 'example.com')
        code, out = self.run_check(f'AGY_VERSION=1.4.5\nAGY_URL={url}\nAGY_SHA512={"c" * 128}\n')
        self.assertEqual(code, 1)
        self.assertIn("AGY_URL is not the address of the vendor's tarball", out)

    def test_the_runtime_image_names_agys_pin(self):
        code, out = self.run_check('GO_VERSION=1.25.13\n', {'image/Dockerfile': 'ARG GO_VERSION=1.25.13\n'})
        self.assertEqual(code, 1)
        for k in ('AGY_VERSION', 'AGY_SHA512'):
            self.assertIn(f'image/Dockerfile: names no ARG {k}', out)
        # The image is given the tarball and downloads none: its address is versions.lock's alone
        # (12.8, since 0.57).
        self.assertNotIn('names no ARG AGY_URL', out)


class RuntimeImageTest(unittest.TestCase):
    # The image build runs agy only as nobody, with no way to gain a privilege back (safety rule 11;
    # the security reviews of c5eff65..c896450): every line of image/Dockerfile that runs agy does so
    # through setpriv.
    def test_agy_runs_only_as_nobody_in_the_build(self):
        text = (HERE.parent / 'image' / 'Dockerfile').read_text()
        runs = [l for l in text.splitlines() if not l.lstrip().startswith('#') and
                ('agy --version' in l or '"agy", "--version"' in l)]
        self.assertEqual(len(runs), 2, runs)
        for l in runs:
            flat = l.replace('", "', ' ').replace('"', '')
            self.assertIn('setpriv --reuid=65534 --regid=65534 --clear-groups --no-new-privs env HOME=/tmp agy --version', flat, l)


class RuntimeImageAgyTest(unittest.TestCase):
    # The runtime image copies agy's tarball from its build context agy, which the server or crew-ctl
    # downloaded and checked, and downloads nothing of agy itself (12.8, 13.6, since 0.57): a stage of
    # that name is empty, so a build given no context fails at the copy and pulls no image named agy.
    def test_agy_comes_from_the_build_context(self):
        text = (HERE.parent / 'image' / 'Dockerfile').read_text()
        lines = [l.strip() for l in text.splitlines() if l.strip() and not l.lstrip().startswith('#')]
        self.assertIn('FROM scratch AS agy', lines)
        self.assertLess(lines.index('FROM scratch AS agy'), next(i for i, l in enumerate(lines) if l.startswith('FROM ${RUNTIME_BASE_IMAGE}')))
        self.assertTrue(any(l.startswith('COPY --from=agy cli_linux_x64.tar.gz ') for l in lines), lines)
        self.assertFalse([l for l in lines if 'AGY_URL' in l or 'storage.googleapis.com' in l])
        self.assertTrue(any('sha512sum -c' in l and 'AGY_SHA512' in l for l in lines))


class RecoveryMatrixTest(unittest.TestCase):
    """The matrix has a row for each row of the design's section 21, numbered in order, its counts
    line adds up to the table (review of M16's close, F1 and F2), and every test function and file
    it names exists in the repository (register row 34)."""

    def test_a_row_for_each_failure_of_21(self):
        import re
        import subprocess
        sec21 = (HERE.parent / 'docs/plan/tdd/21-failure-handling.md').read_text().split('\n')
        failures = [l for l in sec21
                    if l.startswith('| ') and not l.startswith('| ---') and not l.startswith('| Failure |')]
        matrix = (HERE.parent / 'docs/testing/recovery-matrix.md').read_text()
        rows = re.findall(r'^\| (\d+) \|.*\| ([^|]+?) \|$', matrix, re.M)
        self.assertEqual([int(n) for n, _ in rows], list(range(1, len(rows) + 1)))
        self.assertEqual(len(rows), len(failures))
        tested = sum(1 for _, s in rows if s == 'tested')
        owned = sum(1 for _, s in rows if s.startswith('owned by M'))
        missing = sum(1 for _, s in rows if s == 'missing')
        self.assertEqual(tested + owned + missing, len(rows))
        self.assertIn(f'Counts: {tested} tested, {owned} owned by a later milestone, {missing} missing.', matrix)
        root = HERE.parent
        if (root / '.git').exists():
            tracked = subprocess.run(['git', '-C', str(root), 'ls-files'], capture_output=True, text=True, check=True).stdout.splitlines()
        else:
            tracked = [str(p.relative_to(root)) for p in root.rglob('*') if p.is_file()]
        go_tests = set()
        for rel in tracked:
            if rel.endswith('_test.go'):
                go_tests.update(re.findall(r'^func (Test\w+)\(', (root / rel).read_text(), re.M))
        named_funcs = sorted(set(re.findall(r'`(Test[A-Z]\w+)`', matrix)))
        self.assertEqual(sorted(set(named_funcs) - go_tests), [])
        tracked_set = set(tracked)
        tracked_names = {pathlib.Path(rel).name for rel in tracked}
        for ref in sorted(set(re.findall(r'`([a-zA-Z0-9_./-]+_test\.go|[a-zA-Z0-9_./-]+\.(?:spec\.ts|test\.tsx?|sh|py))`', matrix))):
            if '/' in ref:
                self.assertTrue(ref in tracked_set or f'internal/{ref}' in tracked_set, ref)
            else:
                self.assertIn(ref, tracked_names, ref)


class TierSecretsTest(unittest.TestCase):
    """A tier stack's keys and token are the caller's alone, also over the files a run that was cut off
    left, and its test.env goes (security review of 5ddd59a)."""

    def test_the_keys_are_the_callers_over_a_run_that_was_cut_off(self):
        import os
        import stat
        import subprocess
        d = pathlib.Path(tempfile.mkdtemp()) / 'secrets'
        d.mkdir()
        before = {}
        for name in ('test.env', 'jwt_key', 'ingress_token', 'secret_key', 'db_crew'):
            (d / name).write_text('left\n')
            os.chmod(d / name, 0o644)
            before[name] = (d / name).stat().st_ino
        # Another file is held open so a freed inode is not taken again at once by the new one.
        keep = [open(d / name) for name in before]
        subprocess.run(['bash', '-c', '. "$1" && tier_secrets "$2"', 'x', str(HERE / 'tier-secrets.sh'), str(d)], check=True)
        modes = {p.name: stat.S_IMODE(p.stat().st_mode) for p in d.iterdir()}
        self.assertNotIn('test.env', modes)
        for name in ('jwt_key', 'ingress_token', 'secret_key'):
            self.assertEqual(modes[name], 0o600, name)
        for name in ('db_postgres', 'db_crew', 'db_crew_migrate', 'db_crew_read'):
            self.assertEqual(modes[name], 0o644, name)
        self.assertNotEqual((d / 'jwt_key').read_text(), 'left\n')
        # Each secret is a new file, never the old one written over at its old mode (round 2).
        for name in ('jwt_key', 'ingress_token', 'secret_key', 'db_crew'):
            self.assertNotEqual((d / name).stat().st_ino, before[name], name)
        for f in keep:
            f.close()
        for stack in ('hostingtier', 'talktier'):
            text = (HERE / stack / 'stack.sh').read_text()
            self.assertIn('. "$repo/tools/tier-secrets.sh"', text, stack)
            self.assertIn({'hostingtier': 'tier_secrets "$w/secrets"', 'talktier': 'tier_secrets "$w/run"'}[stack], text, stack)
            self.assertNotIn('chmod 0644 "$w/', text, stack)


class TalkTierBaseTest(unittest.TestCase):
    """The talk tier's runtime image is the installed one's, so image/ and harness/ are compared with its
    commit: a dev build's tag names it, and a release with no tag, as updates.publish off makes none,
    names it in its release folder (BUG-017)."""

    def base(self, image, env=None):
        import os
        import subprocess
        e = {k: v for k, v in os.environ.items() if k != 'T20_BASE_COMMIT'}
        e.update(env or {})
        return subprocess.run(['bash', str(HERE / 'talktier' / 'stack.sh'), 'base-commit', image],
                              env=e, capture_output=True, text=True)

    def test_a_release_without_a_tag_is_read_from_its_folder(self):
        import json
        releases = pathlib.Path(tempfile.mkdtemp())
        (releases / '9.8.7').mkdir()
        (releases / '9.8.7' / 'release.json').write_text(json.dumps({'version': '9.8.7', 'commit': '07e0a9f444909a94e5eb4ef6784c4798a5482f32'}))
        r = self.base('crew-runtime:9.8.7', {'T20_RELEASES': str(releases)})
        self.assertEqual((r.returncode, r.stdout.strip()), (0, '07e0a9f444909a94e5eb4ef6784c4798a5482f32'), r.stderr)
        r = self.base('crew-runtime:0.0.0-dev.ae698b46d9f1', {'T20_RELEASES': str(releases)})
        self.assertEqual((r.returncode, r.stdout.strip()), (0, 'ae698b46d9f1'), r.stderr)
        # A release with a tag is its tag, where the clone has one (the gate's may have none); 9.8.7 and
        # 9.9.9 have none.
        import subprocess
        tags = subprocess.run(['git', '-C', str(HERE.parent), 'tag', '--list', 'v[0-9]*.[0-9]*.[0-9]*'],
                              capture_output=True, text=True).stdout.split()
        if tags:
            r = self.base('crew-runtime:' + tags[0][1:], {'T20_RELEASES': str(releases)})
            self.assertEqual((r.returncode, r.stdout.strip()), (0, tags[0]), r.stderr)
        for v in ('9.8.7', '9.9.9'):
            self.assertNotEqual(subprocess.run(['git', '-C', str(HERE.parent), 'rev-parse', '-q', '--verify', f'v{v}^{{commit}}'],
                                               capture_output=True).returncode, 0, v)
        r = self.base('crew-runtime:9.9.9', {'T20_RELEASES': str(releases)})
        self.assertNotEqual(r.returncode, 0)
        self.assertIn('T20_BASE_COMMIT', r.stderr)
        # A folder that names no commit is none.
        (releases / '9.9.9').mkdir()
        (releases / '9.9.9' / 'release.json').write_text(json.dumps({'version': '9.9.9', 'commit': '$(id)'}))
        r = self.base('crew-runtime:9.9.9', {'T20_RELEASES': str(releases)})
        self.assertNotEqual(r.returncode, 0)
        self.assertIn('T20_BASE_COMMIT', r.stderr)
        r = self.base('crew-runtime:9.9.9', {'T20_RELEASES': str(releases), 'T20_BASE_COMMIT': 'abc123'})
        self.assertEqual((r.returncode, r.stdout.strip()), (0, 'abc123'), r.stderr)


class CleanNamesTest(unittest.TestCase):
    def test_secret_names(self):
        for name in ('.env', '.env.filled', 'deploy/crew.env', 'x/secret.key', 'a.pem', 'b.key',
                     'cert.p12', 'cert.pfx', 'store.jks', 'home/.pgpass', 'home/kubeconfig',
                     'home/.claude/.credentials.json', 'antigravity-oauth-token', 'id_ed25519', 'p/terraform.tfstate'):
            self.assertTrue(clean.SECRET_NAMES.search(name), name)

    def test_public_hygiene_checks_work_without_values_file(self):
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            root = pathlib.Path(d)
            (root / 'good.md').write_text(
                'Contact you@example.com; run inside /home/agent/work or <CREW_DATA_DIR>/projects/p1/home/.gemini/skills\n'
            )
            (root / 'bad_email.md').write_text('contact=' + 'alice' + '@' + 'gmail.' + 'com\n')
            (root / 'bad_home.md').write_text(
                'open ' + 'file://' + '/home' + '/devbox/repo\n'
                'see ' + '/home' + '/devbox/work\n'
            )
            hits = clean.check_public_hygiene(['good.md', 'bad_email.md', 'bad_home.md'], root=root)
            self.assertEqual(hits, [
                'bad_email.md:1: personal-email-domain',
                'bad_home.md:1: workstation-home-uri',
                'bad_home.md:2: workstation-home-path',
            ])
            # Ensure findings never leak the matched text itself.
            joined = '\n'.join(hits)
            self.assertNotIn('gmail.' + 'com', joined)
            self.assertNotIn('devbox', joined)

    def test_a_built_program_is_found(self):
        import tempfile
        with tempfile.TemporaryDirectory() as d:
            prog, text = pathlib.Path(d, 'crew-run'), pathlib.Path(d, 'crew-run.go')
            prog.write_bytes(b'\x7fELF\x02\x01\x01' + b'\0' * 64)
            text.write_text('package main\n')
            self.assertTrue(clean.is_program(prog))
            self.assertFalse(clean.is_program(text))
            self.assertFalse(clean.is_program(pathlib.Path(d, 'missing')))

    def test_plain_names(self):
        for name in ('.env.example', 'README.md', 'keys.go', 'deploy/crew-ctl', 'id_ed25519.pub',
                     'internal/credstore/store.go'):
            if name in clean.ALLOWED:
                continue
            self.assertFalse(clean.SECRET_NAMES.search(name), name)


class CleanFilesTest(unittest.TestCase):
    """What check-clean reads: what git lists in a checkout, every file of an exported tree."""

    def tree(self, d):
        root = pathlib.Path(d)
        (root / 'deploy').mkdir()
        (root / 'README.md').write_text('# x\n')
        (root / 'deploy' / 'crew.env').write_text('A=1\n')
        (root / 'ignored.log').write_text('x\n')
        (root / '.gitignore').write_text('ignored.log\n')
        return root

    def test_an_exported_tree_has_every_file_read(self):
        # A check sandbox gets the commit as git archive gives it, with no .git: every file in it is
        # one the commit tracks, so every one is read, the ignored one too.
        with tempfile.TemporaryDirectory() as d:
            root = self.tree(d)
            self.assertEqual(clean.files(False, root), ['.gitignore', 'README.md', 'deploy/crew.env', 'ignored.log'])

    def test_an_exported_trees_link_to_a_file_is_listed_as_git_lists_it(self):
        # A link named like a secret is seen in an export as in a checkout (review of a9532aa).
        with tempfile.TemporaryDirectory() as d:
            root = self.tree(d)
            (root / '.env').symlink_to('README.md')
            (root / 'nowhere').symlink_to('missing')
            self.assertEqual(clean.files(False, root), ['.env', '.gitignore', 'README.md', 'deploy/crew.env', 'ignored.log'])

    def test_staged_needs_a_checkout(self):
        with tempfile.TemporaryDirectory() as d:
            root = self.tree(d)
            with self.assertRaises(SystemExit):
                clean.files(True, root)

    def test_a_checkout_is_read_through_git(self):
        import subprocess
        with tempfile.TemporaryDirectory() as d:
            root = self.tree(d)
            subprocess.run(['git', 'init', '-q', str(root)], check=True)
            self.assertEqual(clean.files(False, root), ['.gitignore', 'README.md', 'deploy/crew.env'])

    def test_a_checkout_whose_git_fails_is_no_export(self):
        # A .git that git cannot read fails the check: it is never taken for an exported tree.
        import subprocess
        with tempfile.TemporaryDirectory() as d:
            root = self.tree(d)
            (root / '.git').write_text('gitdir: /nowhere\n')
            with self.assertRaises(subprocess.CalledProcessError):
                clean.files(False, root)

    def test_staged_reads_index_blobs_not_working_tree(self):
        import subprocess
        with tempfile.TemporaryDirectory() as d:
            root = self.tree(d)
            subprocess.run(['git', 'init', '-q', str(root)], check=True)
            doc = root / 'doc.md'
            prog = root / 'bin'
            doc.write_text('contact=' + 'alice' + '@' + 'gmail.' + 'com\n')
            prog.write_bytes(b'\x7fELF\x02\x01\x01' + b'\0' * 32)
            subprocess.run(['git', '-C', str(root), 'add', 'doc.md', 'bin'], check=True)
            # Clean working tree after staging; --staged must still catch the staged blobs.
            doc.write_text('contact=ok@example.com\n')
            prog.write_text('plain text\n')
            staged_names = clean.files(True, root)
            self.assertEqual(staged_names, ['bin', 'doc.md'])
            self.assertEqual(
                clean.check_public_hygiene(staged_names, root=root, staged=True),
                ['doc.md:1: personal-email-domain'],
            )
            self.assertTrue(clean.is_program_bytes(clean.read_entry_bytes('bin', root=root, staged=True)))
            self.assertEqual(clean.check_public_hygiene(['doc.md'], root=root, staged=False), [])

            # Stage clean content while working tree becomes dirty; --staged must pass.
            subprocess.run(['git', '-C', str(root), 'add', 'doc.md', 'bin'], check=True)
            doc.write_text('contact=' + 'alice' + '@' + 'gmail.' + 'com\n')
            self.assertEqual(clean.check_public_hygiene(clean.files(True, root), root=root, staged=True), [])
            self.assertFalse(clean.is_program_bytes(clean.read_entry_bytes('bin', root=root, staged=True)))



class MakeCheckInASandboxTest(unittest.TestCase):
    """make check in a check sandbox (design 16.2): in Sources the proxy is the only way out, and in a
    microVM a user namespace cannot mount /sys."""

    def dry(self, target, **env):
        import os
        import subprocess
        e = {k: v for k, v in os.environ.items() if k.lower() not in ('https_proxy', 'no_proxy', 'goproxy', 'crew_sandbox')}
        e.update(env)
        return subprocess.run(['make', '-n', '-s', target], cwd=HERE.parent, env=e, capture_output=True, text=True, check=True).stdout

    def test_the_proxy_reaches_the_containers_and_the_check_images_build(self):
        proxy = {'HTTPS_PROXY': 'http://10.0.0.1:3128', 'https_proxy': 'http://10.0.0.1:3128', 'NO_PROXY': '10.0.0.1,localhost', 'no_proxy': '10.0.0.1,localhost'}
        web, go, image = self.dry('test-web', **proxy), self.dry('test-go', **proxy), self.dry('check-image', **proxy)
        for v, val in proxy.items():
            self.assertIn(f"-e '{v}={val}'", web)
            self.assertIn(f"-e '{v}={val}'", go)
            self.assertIn(f"--build-arg '{v}={val}'", image)

    def test_a_proxy_value_is_one_word_of_the_command(self):
        # A value with a space, a quote, or a shell character stays one word: the shell neither splits
        # it nor runs what is in it (review of a9532aa).
        import shlex
        tricky = {'NO_PROXY': "10.0.0.1, localhost", 'HTTPS_PROXY': "http://u:p&x@h:3128/it's$w"}
        for target in ('test-web', 'test-go', 'check-image'):
            words = [w for l in self.dry(target, **tricky).splitlines() if 'PROXY' in l for w in shlex.split(l.rstrip(' \\'))]
            for v, val in tricky.items():
                self.assertTrue(f'{v}={val}' in words, (target, v, words))

    def test_the_check_images_base_images_are_pulled_by_the_daemon_first(self):
        # A build resolves its FROM itself, past the daemon's proxy; a pull goes through it.
        image = self.dry('check-image')
        self.assertIn('pull -q $i', image)
        self.assertLess(image.index('pull -q $i'), image.index('build -q -t'))

    def test_no_proxy_adds_nothing(self):
        for target in ('test-web', 'test-go', 'check-image'):
            self.assertNotIn('PROXY', self.dry(target).upper())

    def test_the_sandboxs_own_proxy_is_not_the_tests(self):
        # A Sources check sandbox runs these tests with its proxy set, GOPROXY among them (13.6, since
        # 0.61): dry() takes each out, or the test above fails there and nowhere else.
        import os
        from unittest import mock
        sandbox = {'HTTPS_PROXY': 'http://10.0.0.1:3128', 'https_proxy': 'http://10.0.0.1:3128', 'NO_PROXY': '10.0.0.1',
                   'no_proxy': '10.0.0.1', 'GOPROXY': 'http://10.0.0.1:3128/goproxy', 'CREW_SANDBOX': 's1'}
        with mock.patch.dict(os.environ, sandbox):
            self.test_no_proxy_adds_nothing()

    def test_the_firewall_test_runs_as_the_sandboxs_root_only_in_a_sandbox(self):
        self.assertIn('unshare -Urnm bash deploy/firewall/test.sh', self.dry('test-firewall'))
        self.assertNotIn('sudo', self.dry('test-firewall'))
        self.assertIn('sudo -n unshare -nm bash deploy/firewall/test.sh', self.dry('test-firewall', CREW_SANDBOX='s1'))

    def test_a_machine_that_refuses_a_user_namespace_is_told_so(self):
        # Ubuntu refuses one to an unprivileged user; unshare alone says only that a write failed.
        self.assertIn('refuses $(id -un) a user namespace', self.dry('test-firewall'))
        self.assertNotIn('refuses', self.dry('test-firewall', CREW_SANDBOX='s1'))

    def test_native_sandbox_mode_skips_check_image_and_uses_crew_cache(self):
        with tempfile.TemporaryDirectory() as cache:
            (pathlib.Path(cache) / 'gocache').mkdir()
            go = self.dry('test-go', CREW_SANDBOX='s1', CREW_CACHE_DIR=cache)
            self.assertNotIn('crew-check:go', go)
            self.assertIn(f'GOCACHE={cache}/gocache', go)
            self.assertNotIn('CREW_TEST_NO_DROP=1', go)
            chk = self.dry('check-go', CREW_SANDBOX='s1', CREW_CACHE_DIR=cache, PKG='./internal/board/...', RUN='^TestProject')
            self.assertIn("-run '^TestProject' ./internal/board/...", chk)
            web = self.dry('test-web', CREW_SANDBOX='s1', CREW_CACHE_DIR=cache)
            self.assertIn(f'npm_config_cache={cache}/npm', web)
            self.assertIn('npm ci --prefer-offline', web)

    def test_postgres_containers_size_shm(self):
        # Docker's default 64 MB /dev/shm runs out under parallel go test -race -p 4 with work_mem=16MB (BUG-035).
        # Exercise check-db.sh in an isolated directory with a fake DOCKER shim to verify its live run argv;
        # hostingtier and talktier stack.sh start full Kata/Docker test stacks outside make check, so strip
        # comments and check their $D run Postgres command directly.
        import os
        import shutil
        import subprocess
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            (root / 'tools').mkdir()
            shutil.copy2(HERE / 'check-db.sh', root / 'tools' / 'check-db.sh')
            shutil.copy2(HERE.parent / 'versions.lock', root / 'versions.lock')
            calls = root / 'docker.log'
            fake_docker = root / 'fake-docker'
            fake_docker.write_text('#!/bin/sh\nprintf "%s\\n" "$*" >> "' + str(calls) + '"\n')
            fake_docker.chmod(0o755)
            env = dict(os.environ, DOCKER=str(fake_docker))
            subprocess.run(['bash', str(root / 'tools' / 'check-db.sh'), 'start'], cwd=root, env=env, check=True, capture_output=True, text=True)
            run_lines = [l for l in calls.read_text().splitlines() if l.startswith('run ')]
            self.assertEqual(len(run_lines), 1, run_lines)
            self.assertIn('--shm-size 1g', run_lines[0])
        for stack in ('hostingtier', 'talktier'):
            code_lines = [l for l in (HERE / stack / 'stack.sh').read_text().splitlines() if not l.lstrip().startswith('#')]
            db_runs = [l for l in code_lines if '$D run' in l and '--name "$DB"' in l]
            self.assertEqual(len(db_runs), 1, (stack, db_runs))
            self.assertIn('--shm-size 256m', db_runs[0], stack)


class PlanCopiesTest(unittest.TestCase):
    def test_plan_copies_date_header_mismatch(self):
        import re
        import shutil
        plan_copies = load('plan-copies')
        tdd = (HERE.parent / 'docs/plan/agent-kanban-tdd.md').read_text(encoding='utf-8')
        m = re.search(r'^- \*\*Date:\*\* (\d{4}-\d{2}-\d{2})$', tdd, re.M)
        self.assertIsNotNone(m)
        with tempfile.TemporaryDirectory() as tmp:
            p = pathlib.Path(tmp)
            for name in ('agent-kanban-tdd.md', 'agent-kanban-requirements.md', 'README.md'):
                shutil.copy2(HERE.parent / 'docs/plan' / name, p / name)
            shutil.copytree(HERE.parent / 'docs/plan/tdd', p / 'tdd')
            req_path = p / 'agent-kanban-requirements.md'
            orig = req_path.read_text(encoding='utf-8')
            req_path.write_text(
                re.sub(r'^- \*\*Date:\*\* \d{4}-\d{2}-\d{2}$', '- **Date:** 1999-01-01', orig, flags=re.M),
                encoding='utf-8',
            )
            bad = plan_copies.check_headers(p, tdd)
            self.assertTrue(
                any(f'agent-kanban-requirements.md says Date 1999-01-01, design is {m.group(1)}' in b for b in bad),
                bad,
            )
            req_path.write_text(
                re.sub(r'^- \*\*Date:\*\* \d{4}-\d{2}-\d{2}$', f'- **Date:** {m.group(1)}', orig, flags=re.M),
                encoding='utf-8',
            )
            bad2 = plan_copies.check_headers(p, tdd)
            self.assertFalse(any('Date' in b for b in bad2), bad2)

    def test_plan_copies_manifest_check(self):
        import shutil
        plan_copies = load('plan-copies')
        tdd = (HERE.parent / 'docs/plan/agent-kanban-tdd.md').read_text(encoding='utf-8')
        with tempfile.TemporaryDirectory() as tmp:
            p = pathlib.Path(tmp)
            shutil.copy2(HERE.parent / 'docs/plan/agent-kanban-tdd.md', p / 'agent-kanban-tdd.md')
            shutil.copytree(HERE.parent / 'docs/plan/tdd', p / 'tdd')
            self.assertEqual(plan_copies.check_manifest(p, tdd), [])

            extra = p / 'tdd' / '29-extra.md'
            extra.write_text('## 29. Extra\n', encoding='utf-8')
            bad_extra = plan_copies.check_manifest(p, tdd)
            self.assertTrue(any('extra files in docs/plan/tdd/' in b for b in bad_extra), bad_extra)
            extra.unlink()

            sec01 = p / 'tdd' / '01-purpose-and-scope.md'
            orig01 = sec01.read_text(encoding='utf-8')
            sec01.write_text('## 1. Wrong title\n', encoding='utf-8')
            bad_head = plan_copies.check_manifest(p, tdd)
            self.assertTrue(any('01-purpose-and-scope.md starts with' in b for b in bad_head), bad_head)
            sec01.unlink()
            bad_missing = plan_copies.check_manifest(p, tdd)
            self.assertTrue(any('missing section files in docs/plan/tdd/' in b for b in bad_missing), bad_missing)
            sec01.write_text(orig01, encoding='utf-8')
            self.assertEqual(plan_copies.check_manifest(p, tdd), [])


if __name__ == '__main__':
    unittest.main(argv=[sys.argv[0]] + sys.argv[1:])
