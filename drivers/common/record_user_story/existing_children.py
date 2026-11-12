"""Observe a resumed session whose recorded children predate this connection."""
import asyncio
import hashlib
import json
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
    bindings = previous.get('historyBindings')
    if not isinstance(bindings, list):
        # Supported legacy captures retain their explicit native file map.
        bindings = []
        for name, value in seed.get('files', {}).items():
            path = source_path(value['source'])
            if sha(path) != value['sha256']:
                raise ValueError('source recording saved data changed')
            if seed['provider'] == 'codex':
                rows = [json.loads(row) for row in read_regular(path).splitlines()]
                identity = next((r['payload']['id'] for r in rows if r.get('type') == 'session_meta'), None)
            else:
                identity = Path(name).stem.removeprefix('agent-')
            bindings.append({'sessionId': identity, 'sha256': value['sha256'], 'source': str(path.relative_to(source))})
    selected = [b for b in bindings if b.get('sessionId') in wanted]
    if len(selected) != len(wanted) or {b['sessionId'] for b in selected} != wanted:
        raise ValueError('frozen seed identities differ from history bindings')
    if not runtime['replay'] and isinstance(previous.get('historyBindings'), list):
        actual = [b for b in plan.get('historyBindings', []) if b.get('sessionId') in wanted]
        if len(actual) != len(selected):
            raise ValueError('parent seed bindings differ from frozen source')
        prefix = ref['path'].rsplit('/', 1)[0] + '/'
        for original in selected:
            expected = dict(original, role='seed', source=prefix + original['source'])
            match = [b for b in actual if b.get('sessionId') == original['sessionId']]
            keys = ('role', 'sessionId', 'parentSessionId', 'childSessionId', 'project', 'nativeTarget', 'source', 'sha256')
            if len(match) != 1 or any(match[0].get(k, '') != expected.get(k, '') for k in keys):
                raise ValueError('parent seed bindings differ from frozen source')
            pinned = seed.get('files', {}).get(original['nativeTarget'], {})
            if pinned.get('source') != expected['source'] or pinned.get('sha256') != original['sha256']:
                raise ValueError('parent seed file binding differs from frozen source')
    manifest = json.loads(read_regular(source / 'manifest.json'))
    for binding in selected:
        path = _seed_path(source, binding['source'])
        if sha(path) != binding['sha256']:
            raise ValueError('source recording saved data changed')
        if manifest.get('materialFormatVersion') == 3 and manifest['files'].get(binding['source']) != binding['sha256']:
            raise ValueError('frozen seed binding differs from source inventory')
        if binding['sessionId'] in children and 'parentSessionId' in binding and binding['parentSessionId'] != parent:
            raise ValueError('frozen seed child belongs to another parent')
    if seed['provider'] == 'claude':
        args = plan.get('args', [])
        if '--resume' not in args or args[args.index('--resume') + 1:args.index('--resume') + 2] != [parent]:
            raise ValueError('native resume must select the frozen seed parent')
    return seed


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
    frames = [json.loads(line) for line in read_regular(capture_root(plan) / 'raw/raw.jsonl', None).splitlines()]
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
