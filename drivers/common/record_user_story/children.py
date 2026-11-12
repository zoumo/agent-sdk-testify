"""Official SDK execution of the explicitly authorized single synthetic child case."""
import asyncio
import json
from pathlib import Path
import sys
import threading
from contextlib import contextmanager

from .child_policy import CHILD_FILE, CHILD_ROLE, CHILD_TEXT, child_task, role_file
from .adapters.common import proxy_command, serial
from .environment import read_regular
from .policy import driver_profile
from .runtime_config import execution_workspace, protocol_project


def limitation(report, message):
    report.setdefault('limitations', []).append(message)


def limitations(report):
    report['facts'] += ['synthetic child task selected; native hook and tool activity retained',
                        'case evidence is assessed independently of capture completion']
    limitation(report, 'parent/child and background facts require actual native source records')


@contextmanager
def codex_child_environment(plan, strict, background):
    marker = Path(plan['root']) / 'tmp' / 'child-root-completed'
    if strict and not plan.get('_replay'):
        config = Path(plan.get('config', Path(plan['root']) / 'config'))
        (config / 'agents').mkdir(parents=True, exist_ok=True)
        (config / 'agents' / 'fixture_reader.toml').write_text(role_file(strict=True, background=background))
        if background:
            marker.parent.mkdir(parents=True, exist_ok=True)
            if marker.exists():
                raise ValueError('stale child completion barrier')
            script = marker.parent / 'child_gate.py'
            script.write_bytes(Path(__file__).with_name('child_gate.py').read_bytes())
            with (config / 'config.toml').open('a') as target:
                target.write('\n[mcp_servers.child_gate]\ncommand=' + json.dumps(sys.executable)
                             + '\nargs=' + json.dumps([str(script), str(marker)])
                             + '\ntool_timeout_sec=70\nenabled_tools=["wait_for_root"]\n'
                             + '[mcp_servers.child_gate.tools.wait_for_root]\napproval_mode="approve"\n')
    try:
        yield marker
    finally:
        if strict and background:
            marker.unlink(missing_ok=True)


def codex(plan, report):
    from openai_codex.client import CodexClient, CodexConfig
    root_id = None
    read_done = threading.Event()
    child_ids = set()
    tool_callers = set()
    tool_turns = {}
    background = driver_profile(plan) == 'background-children'
    strict = plan.get('scenario', {}).get('parameters', {}).get('childTools') == 'inherited-dynamic-v2'
    root_done = False
    limitations(report)

    def answer(method, params):
        report['requests'] += 1
        params = params or {}
        if method in ('item/commandExecution/requestApproval', 'item/fileChange/requestApproval'):
            if strict:
                report['gaps'].append('unexpected native child approval request')
                return {'decision': 'decline'}
            return {'decision': 'accept'}
        if method != 'item/tool/call' or params.get('tool') != 'fixture_read':
            raise NotImplementedError('unsupported native child request: ' + method)
        caller = params.get('threadId')
        if strict and (not caller or caller == root_id or not params.get('turnId')
                       or params.get('arguments') != {'path': CHILD_FILE}
                       or caller in tool_callers or (background and not plan.get('_replay')
                                                    and (not root_done or not marker.is_file()))):
            report['gaps'].append('unauthorized, repeated or pre-root fixture_read')
            return {'contentItems': [{'type': 'inputText', 'text': 'Fixture read is not authorized.'}], 'success': False}
        # The callback reads only the newly generated synthetic file. Native
        # membership evidence is recorded separately, never inferred from this ID.
        content = read_regular(execution_workspace(plan) / CHILD_FILE).decode()
        if strict and background:
            # Subscribe before answering the read: completion may arrive before
            # thread/read returns. Registering after a snapshot loses that event.
            client.register_turn_notifications(params['turnId'])
        tool_callers.add(caller)
        tool_turns.setdefault(caller, set()).add(params.get('turnId'))
        report['receipts'].append({'fixture_read': {'nativeThreadId': caller, 'text': content}})
        read_done.set()
        return {'contentItems': [{'type': 'inputText', 'text': content}], 'success': True}

    config = CodexConfig(launch_args_override=tuple(proxy_command(plan)), cwd=str(execution_workspace(plan)),
                         client_name='official-python-child-case', client_title='Synthetic child read case')
    with codex_child_environment(plan, strict, background) as marker, CodexClient(config=config, approval_handler=answer) as client:
        report['receipts'].append({'initialize': serial(client.initialize())})
        params = {'cwd': plan['project'], 'sandbox': 'workspace-write', 'approvalPolicy': 'on-request', 'ephemeral': False,
                  'baseInstructions': 'Complete the supplied synthetic child task.',
                  'developerInstructions': 'Delegate the fixture read to fixture_reader.',
                  'dynamicTools': [{'type': 'function', 'name': 'fixture_read', 'description': 'Child-only: read the single synthetic fixture exactly once.',
                    'inputSchema': {'type': 'object', 'properties': {'path': {'type': 'string', 'enum': [CHILD_FILE]}}, 'required': ['path'], 'additionalProperties': False}}]}
        if plan['model']:
            params['model'] = plan['model']
        started = client.thread_start(params)
        root_id = started.thread.id
        report['receipts'].append({'thread/start': serial(started)})
        turn = client.turn_start(root_id, plan['prompts'][0])
        report['inputs'] += 1
        while True:
            event = client.next_turn_notification(turn.turn.id)
            report['events'] += 1
            native = serial(event.payload)
            item = native.get('item', {})
            if item.get('type') == 'subAgentActivity':
                if item.get('agentThreadId'):
                    child_ids.add(item['agentThreadId'])
            if item.get('type') == 'collabAgentToolCall':
                child_ids.update(item.get('receiverThreadIds', []))
                if event.method == 'item/completed':
                    report['receipts'].append({'native-collaboration': native})
            if event.method == 'turn/completed' and event.payload.turn.id == turn.turn.id:
                report['results'] += 1
                report['receipts'].append({'root/completed': native})
                if event.payload.turn.status.value == 'failed':
                    raise ValueError('native root child-case turn failed')
                if strict and (native.get('threadId') != root_id or event.payload.turn.status.value != 'completed'):
                    raise ValueError('native root child-case completion identity or status mismatch')
                root_done = True
                if strict and background and not plan.get('_replay'):
                    marker.touch(exist_ok=False)
                break
        post_root_complete = False
        for child in sorted(child_ids):
            try:
                snapshot = serial(client.thread_read(child, include_turns=True))
                report['receipts'].append({'native-child/read': snapshot})
                if snapshot.get('thread', {}).get('id') != child:
                    raise ValueError('native child query returned a different identity')
                if any(turn.get('status') == 'failed' for turn in snapshot['thread'].get('turns', [])):
                    raise RuntimeError('native child turn failed')
                if background:
                    for child_turn in snapshot.get('thread', {}).get('turns', []):
                        if child_turn.get('status') == 'failed':
                            raise RuntimeError('native child turn failed')
                        if child_turn.get('status') == 'inProgress' or (strict and child_turn.get('status') == 'completed'
                                                                     and child_turn.get('id') in tool_turns.get(child, set())):
                            completed = serial(client.wait_for_turn_completed(child_turn['id']))
                            report['receipts'].append({'post-root-native-child/completed': completed})
                            if completed.get('turn', {}).get('status') == 'failed':
                                raise RuntimeError('native child turn failed')
                            if completed.get('threadId') != child or completed.get('turn', {}).get('id') != child_turn['id']:
                                raise ValueError('native child completion returned a different identity')
                            post_root_complete = post_root_complete or completed.get('turn', {}).get('status') == 'completed'
                            snapshot = serial(client.thread_read(child, include_turns=True))
                            report['receipts'].append({'post-root-native-child/read': snapshot})
                thread = snapshot.get('thread', {})
                if thread.get('id') != child:
                    raise ValueError('native child query returned a different identity')
                if child in tool_callers:
                    completed_turns = [turn for turn in thread.get('turns', [])
                                       if turn.get('id') in tool_turns[child] and turn.get('status') == 'completed']
                    output = any(item.get('type') == 'agentMessage' and item.get('text')
                                 for turn in completed_turns for item in turn.get('items', []))
                    if thread.get('parentThreadId') != root_id:
                        report['gaps'].append('native child parent edge to the root is not established')
                    if None in tool_turns[child] or not tool_turns[child].issubset({turn['id'] for turn in completed_turns}):
                        report['gaps'].append('native child fixture-read execution completion is not established')
                    if not output:
                        report['gaps'].append('native completed child output is not established')
                    if thread.get('parentThreadId') == root_id and completed_turns and output:
                        report.setdefault('childEvidence', []).append({'childThreadId': child, 'parentThreadId': root_id,
                            'completedTurnIds': [turn['id'] for turn in completed_turns], 'outputObserved': True})
            except Exception as exc:
                report['receipts'].append({'native-child/read-rejected': {'threadId': child, 'type': type(exc).__name__, 'error': str(exc)}})
                raise
        if not child_ids:
            report['gaps'].append('no child identity confirmed by native collaboration records; child creation untriggered')
        if strict and len(child_ids) != 1:
            report['gaps'].append('exactly one native child identity is required')
        if not read_done.is_set():
            report['gaps'].append('no child fixture_read observed; no read or child output was fabricated')
        if tool_callers - child_ids:
            report['gaps'].append('fixture caller has no confirmed native collaboration membership')
        if background and not post_root_complete:
            report['gaps'].append('native post-root child completion is not established')
        if strict and background and plan.get('_replay'):
            assess_codex_background_order(plan, report, root_id, turn.turn.id, tool_turns)


def assess_codex_background_order(plan, report, root_id, root_turn, tool_turns):
    """Replay dispatch may run ahead of the consumer; use immutable wire order."""
    root_completed = None
    reads = set()
    with (Path(plan['raw']) / 'raw.jsonl').open() as source:
        for index, line in enumerate(source):
            frame = json.loads(line)
            if frame.get('direction') != 's2c':
                continue
            wire = frame.get('wire', {})
            params = wire.get('params', {})
            if (wire.get('method') == 'turn/completed' and params.get('threadId') == root_id
                    and params.get('turn', {}).get('id') == root_turn
                    and params['turn'].get('status') == 'completed'):
                root_completed = index
            if wire.get('method') == 'item/tool/call' and params.get('tool') == 'fixture_read':
                caller, child_turn = params.get('threadId'), params.get('turnId')
                if caller in tool_turns and child_turn in tool_turns[caller]:
                    if root_completed is None or index <= root_completed:
                        report['gaps'].append('native fixture read preceded root completion')
                    reads.add((caller, child_turn))
    if not reads or reads != {(child, turn) for child, turns in tool_turns.items() for turn in turns}:
        report['gaps'].append('native post-root fixture read wire evidence is absent')


async def claude(plan, report):
    from claude_agent_sdk import ClaudeSDKClient, ClaudeAgentOptions, AgentDefinition, Transport, PermissionResultAllow, PermissionResultDeny, ResultMessage
    limitations(report)
    used = set()
    agent_tool_id = None
    child_completed = False
    child_tasks = {}
    events = []
    read_requests = {}
    background = driver_profile(plan) == 'background-children'
    strict = plan.get('scenario', {}).get('parameters', {}).get('backgroundReadBarrier') is True
    root_session = None
    root_released = asyncio.Event()
    aborted = False
    read_pending = False

    class RawTransport(Transport):
        def __init__(self): self.proc = None
        async def connect(self):
            self.proc = await asyncio.create_subprocess_exec(*proxy_command(plan), stdin=asyncio.subprocess.PIPE,
                         stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL)
        async def write(self, data):
            self.proc.stdin.write(data.encode()); await self.proc.stdin.drain()
        async def read_messages(self):
            from .stream import json_messages
            async for message in json_messages(self.proc.stdout):
                yield message
        async def end_input(self):
            if self.proc and self.proc.stdin: self.proc.stdin.close()
        def is_ready(self): return self.proc is not None and self.proc.returncode is None
        async def close(self):
            await self.end_input()
            if self.proc and self.proc.returncode is None:
                try: await asyncio.wait_for(self.proc.wait(),5)
                except asyncio.TimeoutError:
                    self.proc.terminate(); await asyncio.wait_for(self.proc.wait(),5)

    async def permission(tool, values, context):
        nonlocal agent_tool_id, read_pending
        report['requests'] += 1
        if strict:
            allowed_agent = (tool == 'Agent' and not getattr(context, 'agent_id', None) and not agent_tool_id
                             and getattr(context, 'tool_use_id', None)
                             and values.get('subagent_type') == CHILD_ROLE and values.get('run_in_background') is True
                             and values.get('prompt') == child_task('claude', strict=True))
            path = values.get('file_path')
            recorded_project = protocol_project(plan)
            allowed_read = (tool == 'Read' and getattr(context, 'agent_id', None)
                            and getattr(context, 'tool_use_id', None) and agent_tool_id
                            and isinstance(path, str)
                            and (recorded_project / path).resolve() == (recorded_project / CHILD_FILE).resolve()
                            and not read_requests and not read_pending)
            if not allowed_agent and not allowed_read:
                report['gaps'].append('unauthorized or repeated child tool request: ' + tool)
                return PermissionResultDeny(message='Only one fixture_reader child and its fixture Read are authorized.')
            if allowed_read:
                read_pending = True
                try:
                    await asyncio.wait_for(root_released.wait(), 60)
                except asyncio.TimeoutError:
                    report['gaps'].append('root result did not release the child Read')
                    return PermissionResultDeny(message='Root completion barrier timed out.')
                if aborted:
                    return PermissionResultDeny(message='Root execution ended without successful completion.')
        if tool == 'Agent' and getattr(context, 'agent_id', None) is None:
            used.add('Agent')
            agent_tool_id = context.tool_use_id
            report['receipts'].append({'allow-Agent': {'toolUseId': context.tool_use_id}})
        if tool == 'Read' and getattr(context, 'agent_id', None):
            used.add('Read')
            read_requests[context.tool_use_id] = context.agent_id
            report['receipts'].append({'allow-child-Read': {'toolUseId': context.tool_use_id, 'nativeAgentId': context.agent_id}})
        return PermissionResultAllow(updated_input=values)

    def observe(event):
        nonlocal root_session
        report['events'] += 1
        kind, native = type(event).__name__, serial(event)
        events.append({'kind': kind, 'native': native})
        report['receipts'].append({'native-child/event': {'kind': kind, 'native': native}})
        if (kind == 'TaskStartedMessage' or native.get('subtype') == 'task_started') and native.get('tool_use_id') == agent_tool_id:
            if agent_tool_id and native.get('task_id') and native.get('session_id'):
                child_tasks[native['task_id']] = native['session_id']
                if root_session is not None and root_session != native['session_id']:
                    raise ValueError('native child task changed root session')
                root_session = native['session_id']
        if kind == 'TaskNotificationMessage' or native.get('subtype') == 'task_notification':
            if (agent_tool_id and native.get('tool_use_id') == agent_tool_id
                    and native.get('task_id') in child_tasks
                    and native.get('session_id') == child_tasks[native['task_id']]):
                if native.get('status') == 'failed':
                    raise RuntimeError('native child task failed')
                return native.get('status') == 'completed'
        return False

    definition = AgentDefinition(description='Read the synthetic child-fixture.txt and return its token.',
                 prompt=child_task('claude', strict=strict), background=background, model='inherit',
                 **({'tools': ['Read']} if strict else {}))
    options = ClaudeAgentOptions(cwd=str(execution_workspace(plan)), can_use_tool=permission,
               agents={CHILD_ROLE: definition}, include_partial_messages=True)
    async with ClaudeSDKClient(options=options, transport=RawTransport()) as client:
        try:
            await client.query(plan['prompts'][0]); report['inputs'] += 1
            async for event in client.receive_response():
                child_completed = observe(event) or child_completed
                if isinstance(event,ResultMessage):
                    report['results'] += 1; report['receipts'].append({'root/result':serial(event)})
                    if event.is_error: raise ValueError('native root child-case result is_error')
                    if strict:
                        native = serial(event)
                        if not root_session or native.get('session_id') != root_session or native.get('parent_tool_use_id'):
                            raise ValueError('native root result has no matching child task session')
                        root_released.set()
            if background and agent_tool_id and not child_completed:
                async def observe_background():
                    async for event in client.receive_messages():
                        if observe(event):
                            report['receipts'].append({'post-root-native-task':serial(event)})
                            return True
                if not await observe_background():
                    report['gaps'].append('no native post-root child completion observed')
            elif background:
                report['gaps'].append('native child did not complete after the root result')
        finally:
            aborted = True
            root_released.set()
    if 'Agent' not in used: report['gaps'].append('no authorized root Agent permission request observed; child creation is not inferred')
    if 'Read' not in used: report['gaps'].append('no authorized child Read permission observed; no fixed-file read was fabricated')
    assess_claude_child(report, events, agent_tool_id, read_requests, background)


def assess_claude_child(report, events, parent_tool_id, read_requests, background):
    """Relate native task, tool request, output and completion without ID aliases."""
    root = next((item['root/result'].get('session_id') for item in report['receipts'] if 'root/result' in item), None)
    starts = [(index, event['native']) for index, event in enumerate(events)
              if (event['kind'] == 'TaskStartedMessage' or event['native'].get('subtype') == 'task_started')
              and parent_tool_id and event['native'].get('tool_use_id') == parent_tool_id
              and root and event['native'].get('session_id') == root and event['native'].get('task_id')]
    if len({native['task_id'] for _, native in starts}) != 1:
        report['gaps'].append('native child task identity and parent request association are not established')
        return
    started, task = starts[0]
    finished, output, agents = None, [], set()
    root_result = None
    for index, event in enumerate(events):
        kind, native = event['kind'], event['native']
        if kind == 'ResultMessage' and native.get('session_id') == root and root_result is None:
            root_result = index
        if index <= started or native.get('session_id') != root:
            continue
        if kind == 'AssistantMessage' and native.get('parent_tool_use_id') == parent_tool_id:
            for block in native.get('content', []):
                if block.get('name') == 'Read' and block.get('id') in read_requests:
                    agents.add(read_requests[block['id']])
                if block.get('type', 'text') == 'text' and block.get('text'):
                    output.append(index)
        if ((kind == 'TaskNotificationMessage' or native.get('subtype') == 'task_notification')
                and native.get('tool_use_id') == parent_tool_id and native.get('task_id') == task['task_id']
                and native.get('status') == 'completed'):
            finished = index
    if not agents:
        report['gaps'].append('native child Read identity has no correlated parent tool-use context')
    if finished is None:
        report['gaps'].append('native child task completion is not established')
    if not any(finished is not None and index < finished for index in output):
        report['gaps'].append('native child output before its completion is not established')
    if background and (root_result is None or finished is None or finished <= root_result):
        report['gaps'].append('native post-root child completion is not established')
    if not report['gaps']:
        report['childEvidence'] = {'sessionId': root, 'parentToolUseId': parent_tool_id,
                                  'taskId': task['task_id'], 'agentIds': sorted(agents),
                                  'outputObserved': True, 'completed': True}
