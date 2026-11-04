"""Observe a resumed session whose recorded children predate this connection."""
import asyncio
import hashlib
import json
from pathlib import Path

from .adapters.common import proxy_command, serial
from .environment import capture_root, read_regular, recording_temp_root, verify_recording
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



def verify_seed(plan, *, check_source=True):
    seed = plan.get('historySeed')
    if not isinstance(seed, dict) or seed.get('provider') not in ('claude', 'codex') or not seed.get('files'):
        raise ValueError('frozen existing-child seed required')
    root = Path(plan['root'])
    project = Path(seed.get('sourceProject', ''))
    temp_root = Path(plan['tempRoot']) if 'tempRoot' in plan else recording_temp_root()
    if (plan.get('historyProject') != str(project) or not project.is_absolute()
            or project != Path(seed.get('sourceRoot', '')) / 'project'
            or (not plan.get('_nativeCase') and not project.is_relative_to(temp_root))):
        raise ValueError('history scope differs from the frozen source project')
    for name, value in seed['files'].items():
        path = Path(name)
        prefixes = ('history-seed/config/sessions',) if seed['provider'] == 'codex' else ('config/projects', 'history-seed/config/projects')
        if not any(path.is_relative_to(prefix) for prefix in prefixes) or '..' in path.parts or sha(root / path) != value['sha256']:
            raise ValueError('existing-child seed copy changed')
    if check_source:
        if project.resolve(strict=True) != project or project_files(project) != seed.get('projectFiles'):
            raise ValueError('source project changed during read-only observation')
        source = Path(seed['sourceRoot'])
        verify_recording(source)
        for name, digest in seed['sourceFiles'].items():
            if sha(source / name) != digest:
                raise ValueError('source recording identity changed')
        for value in seed['files'].values():
            if sha(source / value['source']) != value['sha256']:
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

    execution_project = plan.get('_fixtureProject', plan['historyProject']) if not check_source else plan['historyProject']
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
