"""Create native history, end setup, then observe the same parent without input."""
import json
from datetime import datetime
from uuid import UUID

from .adapters.common import serial
from .environment import capture_root, read_regular, recording_path
from .existing_children import _legacy_identity, sha
from .runtime_config import execution_workspace, protocol_project
from .scenarios import Untriggered


def frames(root, phase='main'):
    prefix = '' if phase == 'main' else 'remote-probe/'
    return [json.loads(line) for line in read_regular(root / (prefix + 'raw/raw.jsonl')).splitlines()]


def verify_setup(plan, root):
    setup = json.loads(read_regular(root / 'existing-children/setup.json'))
    if (setup.get('schemaVersion') != 1 or setup.get('provider') != plan['provider']
            or not isinstance(setup.get('agentIds'), list) or len(setup['agentIds']) != 1):
        raise ValueError('035 requires its own versioned single-child setup')
    parent, child = setup.get('sessionId'), setup['agentIds'][0]
    if str(UUID(parent)) != parent or not isinstance(child, str) or child == parent:
        raise ValueError('setup parent UUID or child identity differs')
    if str(UUID(child)) != child:
        raise ValueError('setup child UUID differs')
    if (sha(root / 'raw/raw.jsonl') != setup.get('rawSHA256')
            or sha(root / 'raw/transport.json') != setup.get('transportSHA256')):
        raise ValueError('setup protocol or transport bytes changed')
    transport = json.loads(read_regular(root / 'raw/transport.json'))
    if (transport.get('captureStatus') != 'complete' or transport.get('nativeExitCode') != 0
            or any(transport.get(key) is not True for key in
                   ('nativeLaunched', 'clientEOF', 'reaped', 'cleanupComplete'))):
        raise ValueError('setup native process did not finish successfully')
    if plan.get('_replay') and setup != plan.get('existingChildrenSetup'):
        raise ValueError('setup differs from sealed material plan')
    bindings = setup.get('historyBindings', [])
    primary = [binding for binding in bindings if binding.get('nativeTarget', '').endswith('.jsonl')]
    if len(primary) != 2 or {binding.get('sessionId') for binding in primary} != {parent, child}:
        raise ValueError('setup lacks saved native parent and child history')
    for binding in bindings:
        target = binding.get('nativeTarget', '')
        if binding.get('role') != 'seed' or binding.get('source') != 'history/seed/' + target:
            raise ValueError('setup history must be its own immutable pre-observation bytes')
        path = recording_path(root, binding['source'])
        if sha(path) != binding.get('sha256'):
            raise ValueError('setup history bytes changed')
        identity = _legacy_identity(target, read_regular(path), plan['provider'], binding['project'])
        if any(identity.get(key, '') != binding.get(key, '') for key in
               ('sessionId', 'parentSessionId', 'childSessionId', 'project')):
            raise ValueError('setup history differs from native identity')
        if (binding['sessionId'] == parent and binding.get('parentSessionId')
                or binding['sessionId'] == child and binding.get('parentSessionId') != parent):
            raise ValueError('setup native parent-child relationship differs')
    require_setup_wire(plan, frames(root), setup)
    return setup


def require_setup_wire(plan, rows, setup):
    parent, child = setup['sessionId'], setup['agentIds'][0]
    prompt = plan['scenario']['parameters']['setupPrompt']
    outgoing = [(index, row['wire']) for index, row in enumerate(rows) if row['direction'] == 'c2s']
    incoming = [(index, row['wire']) for index, row in enumerate(rows) if row['direction'] == 's2c']
    methods = [wire.get('method') for _, wire in outgoing]
    if methods != ['initialize', 'initialized', 'thread/start', 'turn/start', 'thread/unsubscribe']:
        raise ValueError('setup must create, execute once, and detach exactly one parent')
    replies = {}
    for position, request in outgoing:
        if request['method'] == 'initialized':
            continue
        matches = [(index, wire) for index, wire in incoming
                   if wire.get('id') == request.get('id') and 'result' in wire]
        if len(matches) != 1 or matches[0][0] <= position:
            raise ValueError('setup request lacks a unique causal native response')
        replies[request['method']] = matches[0]
    start_at, start = replies['thread/start']
    input_at, submitted = outgoing[3]
    turn_at, turn = replies['turn/start']
    close_at, closed = outgoing[4]
    if (not start_at < input_at < turn_at < close_at
            or start['result'].get('thread', {}).get('id') != parent
            or start['result'].get('model') != plan['model']
            or submitted['params'].get('threadId') != parent
            or submitted['params'].get('input') != [{'type': 'text', 'text': prompt}]
            or closed.get('params') != {'threadId': parent}
            or replies['thread/unsubscribe'][1]['result'].get('status') != 'unsubscribed'):
        raise ValueError('setup native parent, input, model or detach differs')
    terminal = [(index, wire['params']) for index, wire in incoming
                if wire.get('method') == 'turn/completed'
                and wire.get('params', {}).get('threadId') == parent]
    if (len(terminal) != 1 or terminal[0][1].get('turn', {}).get('id') != turn['result']['turn']['id']
            or terminal[0][1]['turn'].get('status') != 'completed'
            or not turn_at < terminal[0][0] < close_at):
        raise Untriggered('setup root did not complete before detach')
    completed = [(index, wire['params']) for index, wire in incoming
                 if wire.get('method') == 'turn/completed'
                 and wire.get('params', {}).get('threadId') == child
                 and wire['params'].get('turn', {}).get('status') == 'completed']
    relations = [(index, wire['params'].get('item', {})) for index, wire in incoming
                 if wire.get('method') == 'item/completed'
                 and wire.get('params', {}).get('threadId') == parent
                 and wire['params'].get('item', {}).get('type') == 'subAgentActivity'
                 and wire['params']['item'].get('agentThreadId') == child]
    if (len(completed) != 1 or not input_at < completed[0][0] < terminal[0][0]
            or not any(item.get('kind') == 'started' and index < completed[0][0] for index, item in relations)
            or not any(item.get('kind') == 'completed' and completed[0][0] <= index < terminal[0][0]
                       for index, item in relations)):
        raise Untriggered('setup lacks completed native child and confirmed parent edge')


def peer_plan(plan):
    current = dict(plan)
    current['_peer'] = plan['_peer']['probe']
    return current


def setup_codex(plan, report):
    from openai_codex.client import CodexClient, CodexConfig
    from pydantic import BaseModel, ConfigDict
    from .adapters.common import proxy_command
    class Response(BaseModel):
        model_config = ConfigDict(extra='allow')
    config = CodexConfig(launch_args_override=tuple(proxy_command(plan)),
                        cwd=str(execution_workspace(plan)), client_name='official-python-story-recorder',
                        client_title='Protocol recording')
    with CodexClient(config=config) as client:
        client.initialize()
        started = client.thread_start({'cwd': str(protocol_project(plan)), 'model': plan['model'],
            'ephemeral': False, 'sandbox': 'workspace-write', 'approvalPolicy': 'on-request',
            'baseInstructions': 'Complete the supplied synthetic task. Be concise.'})
        identity = started.thread.id
        turn = client.turn_start(identity, plan['scenario']['parameters']['setupPrompt'])
        while True:
            event = client.next_turn_notification(turn.turn.id)
            report['events'] += 1
            if event.method == 'turn/completed' and event.payload.turn.id == turn.turn.id:
                if serial(event.payload).get('turn', {}).get('status') != 'completed':
                    raise Untriggered('setup native root failed')
                break
        client.request('thread/unsubscribe', {'threadId': identity}, response_model=Response)
    return identity


def run(plan, report):
    identity = setup_codex(plan, report)
    root = capture_root(plan)
    setup = verify_setup(plan, root)
    if setup['sessionId'] != identity:
        raise ValueError('setup SDK identity differs from its own saved native history')
    report['receipts'].append({'existing-setup': setup})
    from codex_driver.existing import observe
    current = peer_plan(plan)
    current['_selfSetup'] = setup
    observe(current, report)
    root = capture_root(plan)
    verify_setup(plan, root)
    observation = json.loads(read_regular(root / 'existing-children/observation.json'))
    if (observation.get('schemaVersion') != 1 or observation.get('sessionId') != setup['sessionId']
            or observation.get('setupSHA256') != sha(root / 'existing-children/setup.json')
            or datetime.fromisoformat(observation['startedAt']) <= datetime.fromisoformat(setup['completedAt'])):
        raise ValueError('new observation did not follow the exact completed setup')
    report['receipts'].append({'existing-observation-boundary': observation})
    report['setupInputs'], report['setupResults'] = 1, 1
    report['observationInputs'] = 0
