#!/usr/bin/env python3
"""Check simulation catalog identities, availability and documentation offline."""
from collections import Counter
from pathlib import Path
import json
import re
import sys

ROOT = Path(__file__).resolve().parents[1]


def inventory_errors(text, prefix, expected):
    counts = Counter(re.findall(r"^\| (" + re.escape(prefix) + r"-\d{3}(?:\.\d{2})?) \|", text, re.M))
    actual = set(counts)
    errors = [f"{prefix}: missing {item}" for item in sorted(expected - actual)]
    errors += [f"{prefix}: unexpected {item}" for item in sorted(actual - expected)]
    errors += [f"{prefix}: duplicate {item}" for item in sorted(counts) if counts[item] != 1]
    return errors


def stable_stories(root):
    return re.findall(r'^\| (SDK-US-\d{3}) \|', (root / 'harness/cases.md').read_text(), re.M)


def case_mapping_errors(stories, cases):
    errors, seen = [], set()
    for case in cases:
        if case.get('storyId') not in set(stories):
            errors.append(case['id'] + ': unknown story owner')
        if not re.fullmatch(r'SDK-CASE-\d{3}', case['id']):
            errors.append(case['id'] + ': invalid independent case ID')
        if case['id'] in seen:
            errors.append(case['id'] + ': duplicate case ID')
        seen.add(case['id'])
    return errors


def recording_mapping(root):
    directory = root / 'scenarios'
    index = json.loads((directory / 'index.json').read_text())
    stories = [json.loads((directory / (name + '.json')).read_text()) for name in index['stories']]
    providers = {p: json.loads((directory / 'bindings' / (p + '.json')).read_text()) for p in ('codex', 'claude')}
    cases, rows = [], []
    for story in stories:
        for case in story['cases']:
            cases.append(case)
            routes = []
            for provider, bindings in providers.items():
                if case['implementation'] != 'implemented':
                    routes.append(case['implementation'])
                    continue
                binding = bindings['cases'][case['id']]
                routes.append(dict(case['execution'], **binding.get('execution', {}))['mode'])
            target = f"[配置](../scenarios/{story['id']}.json)"
            requirement = 'required' if case['required'] else 'optional'
            overrides = [provider + ' ' + ('required' if bindings['cases'][case['id']]['required'] else 'optional')
                         for provider, bindings in providers.items()
                         if 'required' in bindings['cases'][case['id']]]
            if overrides:
                requirement += ' (' + ', '.join(overrides) + ')'
            rows.append('| ' + ' | '.join([case['id'], story['id'], requirement, case['goal'], *routes, target]) + ' |')
    return cases, rows


def heading_ids(text):
    counts = Counter()
    anchors = set(re.findall(r'<a\s+id="([^"]+)"\s*>', text))
    for title in re.findall(r"^#{1,6} (.+)$", text, re.M):
        title = re.sub(r"\[([^\]]*)\]\([^)]+\)", r"\1", title)
        slug = re.sub(r"[^\w\- ]", "", title.lower()).replace(" ", "-")
        suffix = f"-{counts[slug]}" if counts[slug] else ""
        anchors.add(slug + suffix)
        counts[slug] += 1
    return anchors


def link_errors(path, text):
    errors = []
    text = re.sub(r"(?ms)^```.*?^```[^\n]*", "", text)
    text = re.sub(r"`[^`]*`", "", text)
    for target in re.findall(r"\[[^\]]*\]\(([^)]+)\)", text):
        if re.match(r"[a-z]+://", target):
            continue
        destination, _, fragment = target.partition("#")
        resolved = path.parent / destination if destination else path
        if not resolved.exists():
            errors.append(f"{path.name}: missing link {target}")
        elif fragment and resolved.suffix == ".md":
            if fragment not in heading_ids(resolved.read_text()):
                errors.append(f"{path.name}: missing heading {target}")
    return errors


def check(root):
    common = {f"SDK-US-{n:03}" for n in range(1, 9)}
    stories = stable_stories(root)
    errors = inventory_errors((root / 'harness/cases.md').read_text(), 'SDK-US', common)
    cases, rows = recording_mapping(root)
    errors += case_mapping_errors(stories, cases)
    case_ids = {case['id'] for case in cases}
    normative = (root / 'harness/cases.md').read_text()
    errors += inventory_errors(normative, 'SDK-CASE', case_ids)
    owners = dict(re.findall(r'^\| (SDK-CASE-\d{3}) \| (SDK-US-\d{3}) \|', normative, re.M))
    requirements = dict(re.findall(r'^\| (SDK-CASE-\d{3}) \| SDK-US-\d{3} \| (required|optional)(?: \([^)]*\))? \|', normative, re.M))
    requirement_labels = dict(re.findall(r'^\| (SDK-CASE-\d{3}) \| SDK-US-\d{3} \| ([^|]+) \|', normative, re.M))
    for row in rows:
        cells = row.split('|')
        if requirement_labels.get(cells[1].strip(), '').strip() != cells[3].strip():
            errors.append(cells[1].strip() + ': normative and provider requirements differ')
    for case in cases:
        if owners.get(case['id']) != case['storyId']:
            errors.append(case['id'] + ': normative and configured ownership differ')
        if case.get('implementation') not in ('implemented', 'planned'):
            errors.append(case['id'] + ': simulation case must be implemented or planned')
        if not isinstance(case.get('required'), bool) or requirements.get(case['id']) != ('required' if case['required'] else 'optional'):
            errors.append(case['id'] + ': normative and configured requirement differ')
    directory = root / 'scenarios'
    index = json.loads((directory / 'index.json').read_text())
    if set(index['stories']) != common or len(index['stories']) != len(common):
        errors.append('Simulation catalog must contain exactly the eight user stories')
    for provider in ('codex', 'claude'):
        bindings = json.loads((directory / 'bindings' / (provider + '.json')).read_text())['cases']
        if set(bindings) != case_ids:
            errors.append(provider + ': bindings must cover exactly the current cases')
        for case in cases:
            binding = bindings.get(case['id'], {})
            route = dict(case.get('execution', {}), **binding.get('execution', {}))
            mode = route.get('mode')
            if mode not in ('live-selected-path', 'planned', 'unsupported'):
                errors.append(provider + '/' + case['id'] + ': invalid simulation route')
            if mode in ('planned', 'unsupported') and not route.get('reason', '').strip():
                errors.append(provider + '/' + case['id'] + ': unavailable route needs a reason')
            if 'offlineTest' in case or 'offlineTest' in binding:
                errors.append(provider + '/' + case['id'] + ': code integration checks are not simulation routes')
    documents = [root / name for name in ('AGENTS.md', 'README.md', 'GLOSSARY.md', 'drivers/README.md')]
    for directory in ('harness', 'docs'):
        documents += sorted((root / directory).rglob('*.md'))
    for path in documents:
        errors += link_errors(path, path.read_text())
    return errors


if __name__ == "__main__":
    failures = check(ROOT)
    for failure in failures:
        print(failure, file=sys.stderr)
    if failures:
        sys.exit(1)
    print(f"Simulation evidence: {len(stable_stories(ROOT))} stories, {len(recording_mapping(ROOT)[0])} cases; identities, availability and documentation valid (offline, no recording claim).")
