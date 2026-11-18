#!/usr/bin/env python3
"""Makes the three copies in docs/plan/ again from the design.

  python3 tools/plan-copies.py           writes build-plan.md, spike-checklist.md, and open-questions.md
  python3 tools/plan-copies.py --check   writes nothing, and fails when a copy or the manifest is not what the design gives

The design, docs/plan/agent-kanban-tdd.md and docs/plan/tdd/*.md, is the source. Run this in the same commit as every change to it.
It needs only Python 3 and its standard library.
"""
import pathlib
import re
import sys

PLAN = pathlib.Path(__file__).resolve().parent.parent / 'docs' / 'plan'
# The parked questions that are worth an answer before a certain point. Every other open one has a default.
FIRST = {72: 'Crew runs unattended'}
MANIFEST_RE = re.compile(r'^\| (\d+) \| \[([^\]]+)\]\((tdd/(\d{2})-[a-z0-9-]+\.md)\) \|', re.M)


def manifest_entries(tdd):
    return [(int(sec), title, rel, int(nn)) for sec, title, rel, nn in MANIFEST_RE.findall(tdd)]


def section(tdd, n, plan=PLAN):
    marker = '\n## %d. ' % n
    if marker in tdd:
        a = tdd.index(marker)
        b = tdd.find('\n## %d. ' % (n + 1))
        return (tdd[a + 1:b] if b > 0 else tdd[a + 1:]).rstrip() + '\n'
    for sec, _, rel, _ in manifest_entries(tdd):
        if sec == n:
            return (plan / rel).read_text(encoding='utf-8').rstrip() + '\n'
    matches = sorted((plan / 'tdd').glob(f'{n:02d}-*.md'))
    if not matches:
        raise ValueError(f'section {n} not found in manifest or {plan / "tdd"}')
    return matches[0].read_text(encoding='utf-8').rstrip() + '\n'


def cell(text):
    return text.replace('|', '\\|')


def copies(tdd, plan=PLAN):
    ver = re.search(r'\*\*Version:\*\* ([\d.]+)', tdd).group(1)
    note = ('> A copy of section %d of `agent-kanban-tdd.md`, version ' + ver +
            '. The design is the source: change it there, and run `python3 tools/plan-copies.py`.\n\n')
    out = {}
    out['build-plan.md'] = '# Build plan\n\n' + note % 23 + section(tdd, 23, plan).split('\n', 1)[1].lstrip()
    out['spike-checklist.md'] = '# Spike checklist (M0)\n\n' + note % 24 + section(tdd, 24, plan).split('\n', 1)[1].lstrip()

    parked = [(int(m.group(1)), m.group(2)) for m in re.finditer(r'^(\d+)\. (.+)$', section(tdd, 25, plan), re.M)
              if not m.group(2).startswith('**Answered') and not m.group(2).startswith('Dropped')]
    rows = [l for l in section(tdd, 28, plan).split('\n') if l.startswith('|') and l.count('|') >= 5]
    state = lambda l: l.split('|')[3].strip()      # the third column of a review table is the state of the finding
    open_rows = [l for l in rows if re.match(r'(Open|Yours to judge)\b', state(l))]
    part_rows = [l for l in rows if l not in open_rows and re.search(r'Partly|Closed in part|for most', state(l))]
    o = ['# Open questions', '',
         '> Made from sections 25 and 28 of `agent-kanban-tdd.md`, version ' + ver + ', by `tools/plan-copies.py`.', '',
         '## Yours to decide', '']
    yours = ['| Q%d | %s | %s |' % (n, cell(q), FIRST[n]) for n, q in parked if n in FIRST]
    if yours:
        o += ['None of these stops the spike (M0). Each has a default that the build goes ahead with.', '',
              '| Question | What is asked | Needed before |', '| --- | --- | --- |'] + yours
    else:
        o += ['None is open now.']
    o += ['', '## Smaller questions, each with a default in the design', '',
          'The number is the number of the parked question in section 25 of the design.', '',
          '| Question | What is asked |', '| --- | --- |']
    o += ['| Q%d | %s |' % (n, cell(q)) for n, q in parked if n not in FIRST]
    head = ['| # | What | State | Where, and what remains |', '| --- | --- | --- | --- |']
    o += ['', '## Open by design', '', 'The reviews named these, and nothing was added for them. The rows come from the review tables of section 28, in their order there.', ''] + head + open_rows
    o += ['', '## Closed in part', '', 'A rule was added for each of these, and something remains. The last column says what.', ''] + head + part_rows
    o += ['', '## Settled only by the spike', '',
          'See `spike-checklist.md`. The second table there says what Crew does if an answer is no.', '']
    out['open-questions.md'] = '\n'.join(o)
    return out


def check_manifest(plan, tdd):
    bad = []
    entries = manifest_entries(tdd)
    want_nums = list(range(1, 29))
    got_nums = [sec for sec, _, _, _ in entries]
    if got_nums != want_nums:
        bad.append(f'agent-kanban-tdd.md manifest sections {got_nums} do not match 1..28 in order')
    for sec, _, rel, nn in entries:
        if sec != nn:
            bad.append(f'agent-kanban-tdd.md manifest section {sec} points at {rel} (number {nn:02d})')
    tdd_dir = plan / 'tdd'
    if not tdd_dir.is_dir():
        bad.append('docs/plan/tdd/ directory is missing')
        return bad
    manifest_rels = [rel for _, _, rel, _ in entries]
    disk_rels = sorted(f'tdd/{p.name}' for p in tdd_dir.iterdir() if p.is_file() and p.name.endswith('.md'))
    missing = [r for r in manifest_rels if r not in disk_rels]
    extra = [r for r in disk_rels if r not in manifest_rels]
    if missing:
        bad.append('missing section files in docs/plan/tdd/: ' + ', '.join(missing))
    if extra:
        bad.append('extra files in docs/plan/tdd/ not in manifest: ' + ', '.join(extra))
    for sec, title, rel, _ in entries:
        fpath = plan / rel
        if fpath.is_file():
            first = fpath.read_text(encoding='utf-8').split('\n', 1)[0]
            want = f'## {sec}. {title}'
            if first != want:
                bad.append(f'{rel} starts with {first!r}, want {want!r}')
    return bad


def check_headers(plan, tdd):
    ver_m = re.search(r'^- \*\*Version:\*\* ([\d.]+)\b', tdd, re.M)
    date_m = re.search(r'^- \*\*Date:\*\* (\d{4}-\d{2}-\d{2})$', tdd, re.M)
    if not ver_m:
        return ['agent-kanban-tdd.md has no Version header']
    if not date_m:
        return ['agent-kanban-tdd.md has no Date header']
    ver, date = ver_m.group(1), date_m.group(1)
    bad = []
    if (plan / 'tdd').exists() or '## Manifest' in tdd:
        bad.extend(check_manifest(plan, tdd))
    req_path = plan / 'agent-kanban-requirements.md'
    if req_path.exists():
        req = req_path.read_text(encoding='utf-8')
        rm = re.search(r'^- \*\*Version:\*\* ([\d.]+), matching version ([\d.]+) of the design$', req, re.M)
        if not rm or rm.group(1) != ver or rm.group(2) != ver:
            bad.append(f'agent-kanban-requirements.md version does not match {ver}')
        rd = re.search(r'^- \*\*Date:\*\* (\d{4}-\d{2}-\d{2})$', req, re.M)
        if not rd or rd.group(1) != date:
            bad.append(f"agent-kanban-requirements.md says Date {rd.group(1) if rd else '?'}, design is {date}")
    readme_path = plan / 'README.md'
    if readme_path.exists():
        readme = readme_path.read_text(encoding='utf-8')
        if f'at design version {ver}.' not in readme:
            bad.append(f'README.md version does not match {ver}')
    return bad


def main():
    args = [a for a in sys.argv[1:] if a != '--check']
    if any(a.startswith('-') for a in args) or len(args) > 1:
        print(__doc__)
        return 0 if args in (['-h'], ['--help']) else 2
    plan = pathlib.Path(args[0]) if args else PLAN
    tdd = (plan / 'agent-kanban-tdd.md').read_text(encoding='utf-8')
    bad = check_headers(plan, tdd)
    if bad and '--check' in sys.argv:
        for b in bad:
            print(b)
        return 1
    out = copies(tdd, plan)
    if '--check' in sys.argv:
        stale = [n for n, text in out.items() if not (plan / n).exists() or (plan / n).read_text(encoding='utf-8') != text]
        if stale:
            print('not made again after the design changed: ' + ', '.join(stale))
            print('run: python3 tools/plan-copies.py')
            return 1
        print('the copies match the design')
        return 0
    if bad:
        for b in bad:
            print(b)
        return 1
    for n, text in out.items():
        (plan / n).write_text(text, encoding='utf-8')
        print('wrote', plan / n)
    return 0


if __name__ == '__main__':
    sys.exit(main())
