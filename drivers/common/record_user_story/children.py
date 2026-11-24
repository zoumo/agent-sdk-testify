"""Official SDK execution of the explicitly authorized single synthetic child case."""
import json
from pathlib import Path
import sys
import threading
from contextlib import contextmanager

from .child_policy import CHILD_FILE, role_file
from .adapters.common import proxy_command, serial
from .environment import read_regular
from .policy import driver_profile
from .runtime_config import execution_workspace


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
