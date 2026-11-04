"""Native interactions driven exclusively by the selected official Python SDKs."""
import asyncio
import json
from pathlib import Path

from .environment import capture_root, replay_receipt, private_json, sdk_identity, stdin_eof_action
from .scenarios import Flow, Untriggered, validate_recipe
from .stimuli import content


def run(plan):
    definition = validate_recipe(plan)
    if definition['mode'] == 'live-selected-path' and definition['driverProfile'] in ('children', 'background-children'):
        return run_child_profile(plan)
    flow = Flow(definition)
    replay = bool(plan.get('_replay'))
    report = {'provider': plan['provider'], 'story': plan['story'], 'status': 'passed',
              'inputs': 0, 'results': 0, 'events': 0, 'requests': 0, 'receipts': [],
              'facts': ['driver uses official Python SDK; raw bytes captured at native process boundary'],
              'case': definition['case'], 'gaps': [], 'captureStatus': 'complete',
              'cliVersion': plan.get('cliVersion')}
    report['scenarioSHA256'] = plan.get('scenarioSHA256')
    report.update(storyId=plan['storyId'], caseId=plan['caseId'])
    report['flowTrace'] = flow.trace
    if definition['mode'] != 'live-selected-path':
        report['captureStatus'] = 'not-executed'
        report['status'] = definition['mode']
        report['gaps'].append(definition['reason'])
    else:
        try:
            report['officialSDK'] = sdk_identity(plan['provider'])
            if definition['driverProfile'] == 'existing-children':
                if plan['provider'] == 'codex':
                    from codex_driver.existing import observe
                    observe(plan, report)
                else:
                    from .existing_children import observe_claude
                    asyncio.run(observe_claude(plan, report))
            elif definition['driverProfile'] == 'remote-close':
                from codex_driver.remote import codex as remote_codex
                remote_codex(plan, report, flow)
            elif plan['provider'] == 'codex':
                from codex_driver.adapter import codex
                codex(plan, report, flow)
            elif plan['provider'] == 'claude':
                from claude_driver.adapter import claude
                asyncio.run(claude(plan, report, flow))
            else:
                raise ValueError('no Python native adapter for provider')
            if flow.active:
                raise RuntimeError('native response ended without a terminal result')
            flow.finish()
            observe_wire(plan, report)
            if stdin_eof_action(plan) == 'interrupt':
                require_interrupt_close(plan, report, replay)
            if definition['driverProfile'] == 'session-close':
                require_closed_process(plan, report, replay)
            report['branchStatus'] = 'passed'
        except Untriggered as exc:
            report['status'] = 'untriggered'
            report['branchStatus'] = 'untriggered'
            report['gaps'].append(str(exc))
        except Exception as exc:
            report['status'] = 'failed'
            report['captureStatus'] = 'failed'
            report['branchStatus'] = 'failed'
            private_json(Path(plan['root']) / ('replay-diagnostics.private.json' if replay else 'diagnostics.private.json'), {'error': str(exc), 'type': type(exc).__name__})
    private_json(Path(plan['root']) / ('replay-report.json' if replay else 'report.json'), report)
    return int(report['captureStatus'] == 'failed')


def run_child_profile(plan):
    """Execute a current child flow and require its native relationship evidence."""
    definition = validate_recipe(plan)
    replay = bool(plan.get('_replay'))
    report = {'provider': plan['provider'], 'story': plan['story'], 'status': 'passed',
              'inputs': 0, 'results': 0, 'events': 0, 'requests': 0, 'receipts': [],
              'facts': ['official SDK child flow; native relationship evidence required'],
              'gaps': [], 'limitations': [], 'case': definition['case'],
              'captureStatus': 'complete', 'cliVersion': plan.get('cliVersion'),
              'storyId': plan['storyId'], 'caseId': plan['caseId'],
              'scenarioSHA256': plan.get('scenarioSHA256')}
    try:
        report['officialSDK'] = sdk_identity(plan['provider'])
        from . import children
        if plan['provider'] == 'codex':
            children.codex(plan, report)
        else:
            asyncio.run(children.claude(plan, report))
        if report['gaps']:
            raise Untriggered('required child evidence absent')
        if report['inputs'] != 1 or report['results'] != 1:
            raise Untriggered('required child root result absent')
        report['branchStatus'] = 'passed'
    except Untriggered as exc:
        report['status'] = 'untriggered'
        report['branchStatus'] = 'untriggered'
        report['gaps'].append(str(exc))
    except Exception as exc:
        report['status'] = 'failed'
        report['captureStatus'] = 'failed'
        private_json(Path(plan['root']) / ('replay-diagnostics.private.json' if replay else 'diagnostics.private.json'),
                     {'error': str(exc), 'type': type(exc).__name__})
    private_json(Path(plan['root']) / ('replay-report.json' if replay else 'report.json'), report)
    return int(report['captureStatus'] == 'failed')


def observe_wire(plan, report):
    """Keep observed native configuration separate from control acknowledgements."""
    from .adapters.common import observe_configuration
    path = capture_root(plan) / 'raw/raw.jsonl'
    if not path.exists():
        parameters = plan['scenario'].get('parameters', {})
        if parameters.get('childConfiguration') or parameters.get('childDetailPolicy'):
            raise Untriggered('native child configuration/detail capture is absent')
        if plan['provider'] == 'codex' and plan['scenario'].get('parameters', {}).get('requireSessionContinuation'):
            raise Untriggered('continuous session raw evidence is absent')
        if stdin_eof_action(plan) == 'interrupt':
            raise Untriggered('native interrupt lifecycle capture is absent')
        return
    frames = [json.loads(line) for line in path.read_bytes().splitlines()]
    from .child_configuration import assess as assess_child_configuration
    assess_child_configuration(plan['provider'], frames, report, plan['scenario'].get('parameters', {}), plan.get('model'))
    if plan['provider'] == 'codex' and plan['scenario'].get('parameters', {}).get('requireSessionContinuation'):
        from .continuation import require_codex_continuation
        require_codex_continuation(frames, report)
    if plan['scenario'].get('parameters', {}).get('requirePostResultEvent'):
        require_post_result_wire(plan['provider'], frames, report)
    if plan['provider'] == 'claude' and plan.get('driverProfile') == 'deferred-approval':
        from .permission_evidence import require_held_permission
        require_held_permission(frames, report)
    if stdin_eof_action(plan) == 'interrupt':
        require_interrupt_wire(frames, report)
    for frame in frames:
        if frame['direction'] != 's2c':
            continue
        native = frame['wire']
        if plan['provider'] == 'codex':
            observe_configuration(report, native.get('method'), native.get('params'))
        else:
            observe_configuration(report, native.get('type'), native)
    from .configuration_evidence import assess
    assess(plan, frames, report)


def require_interrupt_wire(frames, report):
    """Correlate the original cancellation and idle in native receive order."""
    from claude_driver.adapter import cancelled_result
    closed = report.get('interruptClose', {})
    root = closed.get('sessionId')
    idle_message = closed.get('idle', {})
    idle = idle_message.get('data', idle_message)
    results = [item['result'] for item in report['receipts'] if 'result' in item]
    sessions = [item['interrupt-session'] for item in report['receipts'] if 'interrupt-session' in item]
    if (closed.get('ready') is not True or not root or sessions != [root]
            or len(results) != 1 or not cancelled_result(results[0], root)
            or not results[0].get('uuid') or not idle.get('uuid')
            or idle_message.get('subtype') != 'session_state_changed'
            or idle.get('state') != 'idle' or idle.get('session_id') != root
            or idle.get('parent_tool_use_id') is not None):
        raise Untriggered('SDK interrupt cancellation and idle evidence is incomplete')
    incoming = [(i, f['wire']) for i, f in enumerate(frames) if f['direction'] == 's2c']
    outgoing = [(i, f['wire']) for i, f in enumerate(frames) if f['direction'] == 'c2s']
    inits = [(i, w) for i, w in incoming if w.get('type') == 'system' and w.get('subtype') == 'init']
    inputs = [(i, w) for i, w in outgoing if w.get('type') == 'user' and w.get('parent_tool_use_id') is None]
    requests = [(i, w) for i, w in outgoing if w.get('type') == 'control_request'
                and w.get('request', {}).get('subtype') == 'interrupt']
    terminals = [(i, w) for i, w in incoming if w.get('type') == 'result' and w.get('parent_tool_use_id') is None]
    if not (len(inits) == len(inputs) == len(requests) == len(terminals) == 1):
        raise Untriggered('native interrupt requires one session, input, request and terminal result')
    init_at, init = inits[0]
    input_at, submitted = inputs[0]
    request_at, request = requests[0]
    terminal_at, terminal = terminals[0]
    acks = [(i, w) for i, w in incoming if w.get('type') == 'control_response'
            and w.get('response', {}).get('request_id') == request.get('request_id')]
    idles = [(i, w) for i, w in incoming if w.get('type') == 'system'
             and w.get('subtype') == 'session_state_changed' and w.get('state') == 'idle'
             and w.get('session_id') == root and w.get('parent_tool_use_id') is None
             and w.get('uuid') == idle['uuid']]
    if (not request.get('request_id') or init.get('session_id') != root
            or submitted.get('session_id') not in ('default', root)
            or not cancelled_result(terminal, root) or terminal.get('uuid') != results[0]['uuid']
            or terminal.get('terminal_reason') != results[0]['terminal_reason']
            or terminal.get('queued_turn_count', 0) != 0
            or len(acks) != 1 or acks[0][1].get('response', {}).get('subtype') != 'success'
            or len(idles) != 1
            or not (init_at < request_at and input_at < request_at < acks[0][0] < terminal_at < idles[0][0])):
        raise Untriggered('native interrupt acknowledgement, cancellation and same-session idle are not correlated')


def require_interrupt_close(plan, report, replay):
    """Require real process success; a replay proves consumption, not SIGINT."""
    if report.get('interruptClose', {}).get('ready') is not True:
        raise RuntimeError('interrupt close was not ready')
    root = capture_root(plan)
    if replay:
        receipt = replay_receipt(plan)
        complete = (receipt.get('fullConsumption') is True and receipt.get('clientEOF') is True
                    and receipt.get('nativeLaunched') is False)
    else:
        receipt = json.loads((root / 'raw/transport.json').read_text())
        complete = (receipt.get('captureStatus') == 'complete' and receipt.get('reason') == 'EOF'
                    and receipt.get('clientEOF') is True
                    and receipt.get('stdinEOFAction') == 'interrupt' and receipt.get('stdinEOFSignal') == 'SIGINT'
                    and type(receipt.get('nativeExitCode')) is int and receipt['nativeExitCode'] == 0
                    and not receipt.get('nativeSignal')
                    and receipt.get('nativeLaunched') is True and receipt.get('reaped') is True
                    and receipt.get('cleanupComplete') is True)
    if not complete:
        raise RuntimeError('interrupt close lacks successful process or strict replay evidence')
    report['interruptClose']['processClosed'] = True


def require_closed_process(plan, report, replay):
    root = capture_root(plan)
    if replay:
        receipt = replay_receipt(plan)
        complete = receipt.get('fullConsumption') is True and receipt.get('clientEOF') is True and receipt.get('nativeLaunched') is False
    else:
        receipt = json.loads((root / 'raw/transport.json').read_text())
        complete = receipt.get('captureStatus') == 'complete' and receipt.get('cleanupComplete') is True and receipt.get('reaped') is True
    if not complete or not all(report['sessionClose'][key] for key in ('interactionClosed', 'postCloseRejected')):
        raise RuntimeError('session close did not finish interaction and process cleanup')
    report['sessionClose']['processClosed'] = True


def require_post_result_wire(provider, frames, report):
    """Require the first execution's own idle event in native receive order."""
    if provider == 'codex':
        root = next((item['thread/start'].get('thread', {}).get('id')
                     for item in report['receipts'] if 'thread/start' in item), None)
        turn = next((item['input-submitted'].get('turn', {}).get('id')
                     for item in report['receipts'] if 'input-submitted' in item), None)
        if not root or not turn:
            raise Untriggered('native first thread and turn identities are absent')
    else:
        root = next((item['result'].get('session_id')
                     for item in report['receipts'] if 'result' in item), None)
        if not root:
            raise Untriggered('native first result session identity is absent')
    first_result = False
    inputs = 0
    later_idle = False
    for frame in frames:
        native = frame['wire']
        if provider == 'codex':
            params = native.get('params', {})
            same_root = params.get('threadId') == root
            submitted = same_root and native.get('method') == 'turn/start'
            completed = (same_root and native.get('method') == 'turn/completed'
                         and params.get('turn', {}).get('id') == turn)
            idle = (same_root and native.get('method') == 'thread/status/changed'
                    and params.get('status', {}).get('type') == 'idle')
        else:
            # The official SDK uses its default routing alias for query inputs;
            # native result/system messages carry the actual session identity.
            root_input = (native.get('parent_tool_use_id') is None
                          and native.get('session_id') in ('default', root))
            same_root = native.get('session_id') == root and native.get('parent_tool_use_id') is None
            submitted = root_input and native.get('type') == 'user'
            completed = same_root and native.get('type') == 'result'
            idle = (same_root and native.get('type') == 'system'
                    and native.get('subtype') == 'session_state_changed' and native.get('state') == 'idle')
        if frame['direction'] == 'c2s' and submitted:
            inputs += 1
            if inputs == 2:
                break
        elif frame['direction'] == 's2c' and inputs == 1:
            if completed:
                first_result = True
            elif first_result and idle:
                later_idle = True
    if inputs != 2 or not later_idle:
        raise Untriggered('native post-result event before the next input is absent from capture')
