"""Native interactions through the selected Codex or Pi SDK."""
import json
from pathlib import Path

from .environment import capture_root, replay_receipt, private_json, sdk_identity
from .scenarios import Flow, Untriggered, validate_recipe


def run(plan):
    definition = validate_recipe(plan)
    if definition['mode'] == 'live-selected-path' and definition['driverProfile'] in ('children', 'background-children'):
        return run_child_profile(plan)
    flow = Flow(definition)
    replay = bool(plan.get('_replay'))
    report = {'provider': plan['provider'], 'story': plan['story'], 'status': 'passed',
              'inputs': 0, 'results': 0, 'events': 0, 'requests': 0, 'receipts': [],
              'facts': ['driver uses official SDK; raw bytes captured at native process boundary'],
              'case': definition['case'], 'gaps': [], 'captureStatus': 'complete',
              'cliVersion': plan.get('cliVersion')}
    report['scenarioSHA256'] = plan.get('scenarioSHA256')
    report.update(storyId=plan['storyId'], caseId=plan['caseId'])
    report['flowTrace'] = flow.trace
    if definition['mode'] != 'live-selected-path':
        report['captureStatus'] = 'not-executed'
        report['status'] = definition['mode']
        report['branchStatus'] = definition['mode']
        report['gaps'].append(definition['reason'])
    else:
        try:
            if plan['provider'] != 'pi':
                report['officialSDK'] = sdk_identity(plan['provider'])
            if plan['provider'] == 'pi':
                if plan['caseId'] == 'SDK-CASE-001':
                    from pi_driver.continuation import run as run_pi
                else:
                    from pi_driver.base import run as run_pi
                run_pi(plan, report, flow)
            elif definition['driverProfile'] == 'existing-children':
                if definition.get('parameters', {}).get('selfSeed') and not plan.get('historySeed'):
                    from .self_seeded_children import run as self_seeded
                    self_seeded(plan, report)
                else:
                    from codex_driver.existing import observe
                    observe(plan, report)
            elif definition['driverProfile'] == 'remote-close':
                from codex_driver.remote import codex as remote_codex
                remote_codex(plan, report, flow)
            else:
                from codex_driver.adapter import codex
                codex(plan, report, flow)
            if flow.active:
                raise RuntimeError('native response ended without a terminal result')
            flow.finish()
            observe_wire(plan, report)
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
        children.codex(plan, report)
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
        if plan['provider'] == 'pi':
            raise Untriggered('Pi native protocol capture is absent')
        if plan.get('driverProfile') in ('mcp-tool-approval', 'sandbox-permissions'):
            raise Untriggered('native approval capture is absent')
        if plan.get('driverProfile') == 'context-injection':
            raise Untriggered('native context injection capture is absent')
        parameters = plan['scenario'].get('parameters', {})
        if parameters.get('childConfiguration') or parameters.get('childDetailPolicy'):
            raise Untriggered('native child configuration/detail capture is absent')
        if parameters.get('requireSessionContinuation'):
            raise Untriggered('continuous session raw evidence is absent')
        return
    frames = [json.loads(line) for line in path.read_bytes().splitlines()]
    if plan['provider'] == 'pi':
        if plan['caseId'] == 'SDK-CASE-001':
            from pi_driver.continuation import require_wire
        else:
            from pi_driver.base import require_wire
        require_wire(frames, report)
        return
    if plan.get('driverProfile') == 'mcp-tool-approval':
        from .mcp_approval_evidence import assess
        assess(plan, frames, report)
    if plan.get('driverProfile') == 'sandbox-permissions':
        from .sandbox_permission_evidence import assess
        assess(plan, frames, report)
    if plan.get('driverProfile') == 'context-injection':
        from .context_injection import assess
        assess(plan, frames, report)
    from .child_configuration import assess as assess_child_configuration
    parameters = plan['scenario'].get('parameters', {})
    assess_child_configuration(frames, report, parameters, plan.get('model'))
    if parameters.get('requireSessionContinuation'):
        from .continuation import require_codex_continuation
        require_codex_continuation(frames, report)
    if parameters.get('requirePostResultEvent'):
        require_post_result_wire(frames, report)
    for frame in frames:
        if frame['direction'] != 's2c':
            continue
        native = frame['wire']
        observe_configuration(report, native.get('method'), native.get('params'))
    from .configuration_evidence import assess
    assess(plan, frames, report)


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


def require_post_result_wire(frames, report):
    """Require the first execution's own idle event in native receive order."""
    root = next((item['thread/start'].get('thread', {}).get('id')
                 for item in report['receipts'] if 'thread/start' in item), None)
    turn = next((item['input-submitted'].get('turn', {}).get('id')
                 for item in report['receipts'] if 'input-submitted' in item), None)
    if not root or not turn:
        raise Untriggered('native first thread and turn identities are absent')
    first_result = False
    inputs = 0
    later_idle = False
    for frame in frames:
        native = frame['wire']
        params = native.get('params', {})
        same_root = params.get('threadId') == root
        submitted = same_root and native.get('method') == 'turn/start'
        completed = (same_root and native.get('method') == 'turn/completed'
                     and params.get('turn', {}).get('id') == turn)
        idle = (same_root and native.get('method') == 'thread/status/changed'
                and params.get('status', {}).get('type') == 'idle')
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
