"""Observe a resumed session whose recorded children predate this connection."""
import asyncio
import hashlib
import json
import re
from pathlib import Path

from .adapters.common import proxy_command, serial
from .environment import capture_root, read_regular, recording_temp_root, verify_recording, recording_path as _seed_path
from .runtime_config import execution_workspace
from .scenarios import Untriggered


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


def _verify_canonical_seed(plan, seed):
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


def verify_seed(plan, *, check_source=True):
    seed = plan.get('historySeed')
    if isinstance(seed, dict) and 'materialPlan' in seed:
        return _verify_canonical_seed(plan, seed)
    if not isinstance(seed, dict) or seed.get('provider') not in ('claude', 'codex') or not seed.get('files'):
        raise ValueError('frozen existing-child seed required')
    if seed.get('projectSnapshot') not in (None, 'history-seed/project'):
        raise ValueError('invalid seed project snapshot')
    root = Path(plan.get('_seedRoot') or plan['root'])
    project = Path(seed.get('sourceProject', ''))
    temp_root = Path(plan['tempRoot']) if 'tempRoot' in plan else recording_temp_root()
    legacy_source = 'sourceRoot' in seed
    source_reference = seed.get('sourceRootRef')
    if not legacy_source and (not isinstance(source_reference, str) or Path(source_reference).is_absolute()
                              or '..' in Path(source_reference).parts):
        raise ValueError('frozen seed source reference is missing or invalid')
    if (plan.get('historyProject') != str(project) or not project.is_absolute()
            or (legacy_source and not seed.get('projectSnapshot')
                and project != Path(seed.get('sourceRoot', '')) / 'project')
            or (legacy_source and not plan.get('_nativeCase') and not project.is_relative_to(temp_root))):
        raise ValueError('history scope differs from the frozen source project')
    for name, value in seed['files'].items():
        path = Path(name)
        prefixes = ('history-seed/config/sessions',) if seed['provider'] == 'codex' else ('config/projects', 'history-seed/config/projects')
        if not any(path.is_relative_to(prefix) for prefix in prefixes) or '..' in path.parts or sha(root / path) != value['sha256']:
            raise ValueError('existing-child seed copy changed')
    snapshot = root / seed['projectSnapshot'] if seed.get('projectSnapshot') else project
    if seed.get('projectSnapshot') and project_files(snapshot) != seed.get('projectFiles'):
        raise ValueError('source project changed during read-only observation')
    if legacy_source and check_source and not seed.get('projectSnapshot') and project.resolve(strict=True) != project:
        raise ValueError('source project changed during read-only observation')
    if not legacy_source or check_source:
        source = root / source_reference if not legacy_source else Path(seed['sourceRoot'])
        verify_recording(source)
        for name, digest in seed['sourceFiles'].items():
            if sha(source / name) != digest:
                raise ValueError('source recording identity changed')
        for value in seed['files'].values():
            source_path = root / value['source'] if not legacy_source else source / value['source']
            if sha(source_path) != value['sha256']:
                raise ValueError('source recording saved data changed')
    return seed


async def observe_claude(plan, report):
    from claude_agent_sdk import ClaudeSDKClient, ClaudeAgentOptions, Transport, list_subagents
    from .stream import json_messages
    # Replay owns frozen copies and their original source digests. It must not
    # require the old recording directory to remain available after capture.
    check_source = not bool(plan.get('_replay'))
    seed = verify_seed(plan, check_source=check_source)
    if '--resume' not in plan['args'] or plan['args'][plan['args'].index('--resume') + 1] != seed['sessionId']:
        raise ValueError('native resume must select the frozen seed parent')

    class RawTransport(Transport):
        def __init__(self): self.proc = None
        async def connect(self):
            self.proc = await asyncio.create_subprocess_exec(*proxy_command(plan), stdin=asyncio.subprocess.PIPE,
                         stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL)
        async def write(self, data):
            self.proc.stdin.write(data.encode()); await self.proc.stdin.drain()
        async def read_messages(self):
            async for value in json_messages(self.proc.stdout): yield value
        async def end_input(self):
            if self.proc and self.proc.stdin: self.proc.stdin.close()
        def is_ready(self): return self.proc is not None and self.proc.returncode is None
        async def close(self):
            await self.end_input()
            if self.proc and self.proc.returncode is None:
                try: await asyncio.wait_for(self.proc.wait(), 5)
                except asyncio.TimeoutError:
                    self.proc.terminate(); await asyncio.wait_for(self.proc.wait(), 5)
            if self.proc and self.proc.returncode:
                raise RuntimeError('existing-child observation transport failed')

    execution_project = execution_workspace(plan)
    options = ClaudeAgentOptions(cwd=execution_project, resume=seed['sessionId'], forward_subagent_text=True)
    async with ClaudeSDKClient(options=options, transport=RawTransport()) as client:
        async def wait_observed():
            async for event in client.receive_messages():
                report['events'] += 1
                native = serial(event)
                data = native.get('data', native)
                if (data.get('session_id') == seed['sessionId'] and data.get('hook_name') == 'SessionStart:resume'
                        and data.get('hook_event') == 'SessionStart' and data.get('uuid')
                        and native.get('subtype', data.get('subtype')) in ('hook_started', 'hook_response')):
                    return native
        try:
            observed = await asyncio.wait_for(wait_observed(), 10)
        except asyncio.TimeoutError as exc:
            raise Untriggered('resume did not establish observed native parent before any input') from exc
        if observed is None:
            raise Untriggered('resume ended without observed native parent')
        report['receipts'].append({'existing-observation': observed})
        verify_seed(plan, check_source=check_source)
        agents = list_subagents(seed['sessionId'], directory=execution_project)
        if agents != seed['agentIds']:
            raise Untriggered('resumed observation did not discover the exact pre-existing children')
        report['receipts'].append({'existing-children': {'sessionId': seed['sessionId'], 'agentIds': agents,
                                                       'sourceFiles': seed['files']}})
    verify_seed(plan, check_source=check_source)
    require_observation_wire(plan, report)


def require_observation_wire(plan, report):
    raw = 'remote-probe/raw/raw.jsonl' if plan.get('_observationPhase') == 'probe' else 'raw/raw.jsonl'
    frames = [json.loads(line) for line in read_regular(capture_root(plan) / raw, None).splitlines()]
    requests = [frame['wire'] for frame in frames if frame['direction'] == 'c2s']
    if (len(requests) != 1 or requests[0].get('type') != 'control_request'
            or requests[0].get('request', {}).get('subtype') != 'initialize'):
        raise ValueError('existing-child observation must not submit new input or create children')
    seed = plan['historySeed']
    observed = next((item['existing-observation'] for item in report['receipts'] if 'existing-observation' in item), {})
    observed = observed.get('data', observed)
    matches = [frame for frame in frames if frame['direction'] == 's2c'
               and frame['wire'].get('uuid') == observed.get('uuid')
               and frame['wire'].get('session_id') == seed['sessionId']
               and frame['wire'].get('hook_name') == 'SessionStart:resume']
    if len(matches) != 1:
        raise ValueError('existing-child observer receipt lacks a unique native resume hook')
