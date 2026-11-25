"""Check frozen seed files in the controlled history peers."""
import hashlib
import json
import os
import re
import stat
from pathlib import Path


def read_regular(path, limit=None):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or (limit is not None and info.st_size > limit):
            raise ValueError('regular file required; requested read limit must not be exceeded')
        with os.fdopen(fd, 'rb', closefd=False) as source:
            return source.read() if limit is None else source.read(limit + 1)
    finally:
        os.close(fd)


def _seed_path(root, reference):
    if (not isinstance(reference, str) or not reference or "\\" in reference
            or "\x00" in reference or str(Path(reference)) != reference
            or Path(reference).is_absolute() or any(x in ('.', '..') for x in reference.split('/'))):
        raise ValueError('invalid frozen seed reference')
    current = root
    for part in reference.split('/'):
        current = current / part
        if current.is_symlink():
            raise ValueError('frozen seed reference traverses symlink')
    return current


def verify_recording(root):
    item = json.loads(read_regular(root / 'manifest.json'))
    if item.get('materialFormatVersion') == 3:
        files = item.get('files')
        if not isinstance(files, dict) or not files:
            raise ValueError('material inventory required')
        actual = {}
        for path in root.rglob('*'):
            if path.is_symlink():
                raise ValueError('material inventory traverses symlink')
            if path.is_dir():
                continue
            if not path.is_file():
                raise ValueError('material inventory has an unsupported entry')
            name = path.relative_to(root).as_posix()
            if name not in ('manifest.json', 'material.json'):
                actual[name] = hashlib.sha256(read_regular(_seed_path(root, name), None)).hexdigest()
        if actual != files:
            raise ValueError('material inventory changed')
        if hashlib.sha256(read_regular(root / 'plan.json')).hexdigest() != item.get('planSHA256'):
            raise ValueError('recording plan changed')
        return
    if item.get('remotePhaseFiles'):
        raise ValueError('unexpected remote phases in child history fixture')
    if item.get('planSHA256') and hashlib.sha256(read_regular(root / 'plan.json')).hexdigest() != item['planSHA256']:
        raise ValueError('recording plan changed')
    for name, digest in item['files'].items():
        path = root / 'raw' / name
        if Path(name).name != name or path.is_symlink() or hashlib.sha256(read_regular(path, None)).hexdigest() != digest:
            raise ValueError('recording changed')
    for name, digest in item.get('caseFiles', {}).items():
        if name not in ['project/child-fixture.txt', 'config/agents/fixture_reader.toml'] or hashlib.sha256(read_regular(root / name)).hexdigest() != digest:
            raise ValueError('synthetic child case file changed')
    for name, digest in item.get('driverFiles', {}).items():
        path = root / name
        if not name.startswith('driver/') or '..' in Path(name).parts or path.is_symlink() or hashlib.sha256(read_regular(path)).hexdigest() != digest:
            raise ValueError('recording driver snapshot changed')
    for name, digest in item.get('savedFiles', {}).items():
        path = root / name
        if not name.startswith(('config/projects/', 'config/sessions/')) or '..' in Path(name).parts or path.is_symlink() or hashlib.sha256(read_regular(path)).hexdigest() != digest:
            raise ValueError('saved recording source changed')





def sha(path):
    return hashlib.sha256(read_regular(path, None)).hexdigest()


def project_files(project):
    """Pin the synthetic historical cwd without permitting native writes to it."""
    result = {}
    for path in sorted(project.rglob('*')):
        if path.is_symlink() or not (path.is_dir() or path.is_file()):
            raise ValueError('seed project contains an unsupported filesystem entry')
        result[str(path.relative_to(project))] = 'directory' if path.is_dir() else sha(path)
    return result


def _legacy_identity(target, data, provider, project):
    """Read native identities without rewriting the frozen legacy bytes."""
    prefix = 'config/sessions/' if provider == 'codex' else 'config/projects/'
    if not target.startswith(prefix):
        raise ValueError('seed native target differs from provider')
    if provider == 'codex':
        row = json.loads(data.split(b'\n', 1)[0])
        payload = row.get('payload', {})
        identity = payload.get('id')
        if (row.get('type') != 'session_meta' or not isinstance(identity, str) or not identity
                or not Path(target).name.endswith(identity + '.jsonl')):
            raise ValueError('seed native filename differs from session identity')
        origin = payload.get('source')
        origin = origin if isinstance(origin, dict) else {}
        parent = (payload.get('parentThreadId') or payload.get('parent_thread_id')
                  or origin.get('subagent', {}).get('thread_spawn', {}).get('parent_thread_id', ''))
        return {'sessionId': identity, 'parentSessionId': parent, 'childSessionId': identity if parent else '',
                'project': payload.get('cwd') or project}
    rows = [json.loads(line) for line in data.splitlines() if line.strip()]
    path = Path(target)
    parent = path.parent.parent.name if path.parent.name == 'subagents' else ''
    identity = path.name.removesuffix('.meta.json').removesuffix('.jsonl')
    if parent:
        identity = identity.removeprefix('agent-')
    elif not target.endswith('.jsonl'):
        raise ValueError('unsupported seed native file')
    projects = {r['cwd'] for r in rows if r.get('cwd')}
    if len(projects) > 1:
        raise ValueError('seed native project identity differs')
    project = next(iter(projects), project)
    if not target.startswith('config/projects/' + re.sub(r'[^a-zA-Z0-9]', '-', project) + '/'):
        raise ValueError('seed native target differs from project identity')
    for row in rows:
        if (row.get('sessionId', parent or identity) != (parent or identity)
                or (parent and row.get('agentId', identity) != identity)):
            raise ValueError('seed native filename differs from parent or child identity')
    return {'sessionId': identity, 'parentSessionId': parent, 'childSessionId': identity if parent else '',
            'project': project}


def _legacy_seed_bindings(source, manifest, provider, project):
    saved = manifest.get('savedFiles')
    if not isinstance(saved, dict) or not saved:
        raise ValueError('frozen legacy saved file inventory required')
    bindings = []
    for target, digest in saved.items():
        path = _seed_path(source, target)
        data = read_regular(path)
        if hashlib.sha256(data).hexdigest() != digest:
            raise ValueError('source recording saved data changed')
        native_project = project
        if target.endswith('.meta.json'):
            sibling = target.removesuffix('.meta.json') + '.jsonl'
            if sibling not in saved:
                raise ValueError('seed sidecar lacks saved native history')
            native_project = _legacy_identity(sibling, read_regular(_seed_path(source, sibling)),
                                              provider, project)['project']
        identity = _legacy_identity(target, data, provider, native_project)
        bindings.append(dict(identity, nativeTarget=target, source=target, sha256=digest))
    return bindings


def verify_seed(plan):
    seed = plan["historySeed"]
    runtime = plan.get('_runtime', {})
    root = Path(runtime.get('seedRoot', ''))
    if not root.is_absolute() or not isinstance(runtime.get('replay'), bool):
        raise ValueError('frozen seed requires runtime source root')

    def source_path(reference):
        # Material references retain their canonical prefix. Before capture,
        # the same closure lives at the explicitly supplied fixture source root.
        _seed_path(root, reference)
        prefix = 'history/seed/'
        if not reference.startswith(prefix):
            raise ValueError('seed reference is outside frozen history')
        return _seed_path(root, reference if runtime['replay'] else reference[len(prefix):])

    ref = seed['materialPlan']
    if not isinstance(ref, dict) or sha(source_path(ref.get('path'))) != ref.get('sha256'):
        raise ValueError('frozen seed source plan changed')
    source = source_path(ref['path']).parent
    previous = json.loads(read_regular(source / 'plan.json'))
    if (seed.get('provider') != plan.get('provider') or previous.get('provider') != seed['provider']
            or previous.get('driverProfile') != 'history'
            or previous.get('scenario', {}).get('parameters', {}).get('historyTarget') != 'child-messages'):
        raise ValueError('frozen seed provider or history case differs')
    project = previous.get('_fixtureProject') if seed['provider'] == 'claude' else None
    project = project or previous.get('project')
    if (not isinstance(project, str) or not Path(project).is_absolute()
            or seed.get('sourceProject') != project or plan.get('historyProject') != project):
        raise ValueError('history scope differs from the frozen source project')
    verify_recording(source)
    if runtime['replay']:
        verify_recording(root)
        sealed = json.loads(read_regular(root / 'plan.json'))
        if any(plan.get(k) != sealed.get(k) for k in ('historySeed', 'historyBindings', 'historyProject', 'provider')):
            raise ValueError('replay seed facts differ from sealed material plan')
    else:
        inventory = seed.get('sourceFiles')
        if not isinstance(inventory, dict) or not inventory:
            raise ValueError('frozen seed closure inventory required')
        for name, digest in inventory.items():
            if sha(_seed_path(root, name)) != digest:
                raise ValueError('source recording identity changed')
        if project_files(root / 'project') != seed.get('projectFiles'):
            raise ValueError('source project changed during read-only observation')

    parent, children = seed.get('sessionId'), seed.get('agentIds')
    if (not isinstance(parent, str) or not parent or not isinstance(children, list)
            or not children or any(not isinstance(x, str) or not x or x == parent for x in children)
            or len(set(children)) != len(children)):
        raise ValueError('frozen parent and child identities required')
    wanted = {parent, *children}
    manifest = json.loads(read_regular(source / 'manifest.json'))
    legacy = manifest.get('materialFormatVersion') != 3
    bindings = (_legacy_seed_bindings(source, manifest, seed['provider'], project) if legacy
                else previous.get('historyBindings'))
    if not isinstance(bindings, list):
        raise ValueError('frozen seed history bindings required')
    selected = [b for b in bindings if b.get('sessionId') in wanted]
    primary = [b for b in selected if b.get('nativeTarget', '').endswith('.jsonl')]
    if len(primary) != len(wanted) or {b['sessionId'] for b in primary} != wanted:
        raise ValueError('frozen seed identities differ from history bindings')
    prefix = ref['path'].rsplit('/', 1)[0] + '/'
    if legacy:
        for original in selected:
            if not runtime['replay']:
                # Preparation carries a transient file view; it selects primary
                # JSONL records only. Sidecars remain in the complete closure.
                if original not in primary:
                    continue
                pinned = seed.get('files', {}).get(original['nativeTarget'], {})
                if pinned != {'source': prefix + original['source'], 'sha256': original['sha256']}:
                    raise ValueError('parent seed file binding differs from frozen source')
                continue
            matches = [b for b in plan.get('historyBindings', [])
                       if b.get('nativeTarget') == original['nativeTarget']]
            if len(matches) != 1:
                raise ValueError('frozen seed identities differ from history bindings')
            binding = matches[0]
            keys = ('sessionId', 'parentSessionId', 'childSessionId', 'project')
            if any(binding.get(k, '') != original.get(k, '') for k in keys):
                raise ValueError('seed binding differs from frozen native identity')
            if binding.get('role') == 'seed':
                if (binding.get('source') != prefix + original['source']
                        or binding.get('sha256') != original['sha256']):
                    raise ValueError('seed binding differs from frozen source')
            elif binding.get('role') == 'runtime':
                if binding.get('source') != 'history/runtime/' + original['nativeTarget']:
                    raise ValueError('seed runtime binding differs from native target')
            else:
                raise ValueError('unsupported seed binding role')
            path = _seed_path(root, binding['source'])
            if sha(path) != binding.get('sha256'):
                raise ValueError('seed binding saved data changed')
            native_project = original['project']
            actual = _legacy_identity(binding['nativeTarget'], read_regular(path), seed['provider'], native_project)
            if any(actual.get(k, '') != original.get(k, '') for k in keys):
                raise ValueError('seed binding differs from native file identity')
    if not runtime['replay'] and isinstance(previous.get('historyBindings'), list):
        actual = [b for b in plan.get('historyBindings', []) if b.get('sessionId') in wanted]
        if len(actual) != len(selected):
            raise ValueError('parent seed bindings differ from frozen source')
        prefix = ref['path'].rsplit('/', 1)[0] + '/'
        for original in selected:
            expected = dict(original, role='seed', source=prefix + original['source'])
            match = [b for b in actual if b.get('nativeTarget') == original['nativeTarget']]
            keys = ('role', 'sessionId', 'parentSessionId', 'childSessionId', 'project', 'nativeTarget', 'source', 'sha256')
            if len(match) != 1 or any(match[0].get(k, '') != expected.get(k, '') for k in keys):
                raise ValueError('parent seed bindings differ from frozen source')
            pinned = seed.get('files', {}).get(original['nativeTarget'], {})
            if original in primary and (pinned.get('source') != expected['source']
                                        or pinned.get('sha256') != original['sha256']):
                raise ValueError('parent seed file binding differs from frozen source')
    for binding in selected:
        path = _seed_path(source, binding['source'])
        if sha(path) != binding['sha256']:
            raise ValueError('source recording saved data changed')
        if manifest.get('materialFormatVersion') == 3 and manifest['files'].get(binding['source']) != binding['sha256']:
            raise ValueError('frozen seed binding differs from source inventory')
        if (binding['sessionId'] in children and binding.get('parentSessionId') != parent
                or binding['sessionId'] == parent and binding.get('parentSessionId')):
            raise ValueError('frozen seed child belongs to another parent')
    if seed['provider'] == 'claude':
        args = plan.get('args', [])
        if '--resume' not in args or args[args.index('--resume') + 1:args.index('--resume') + 2] != [parent]:
            raise ValueError('native resume must select the frozen seed parent')
    if 'files' in seed:
        return seed
    # Receipts consume a validated transient view; the sealed core keeps the
    # binding map as its only owner of native file references.
    return dict(seed, files={b['nativeTarget']: {'source': prefix + b['source'], 'sha256': b['sha256']}
                             for b in primary})
