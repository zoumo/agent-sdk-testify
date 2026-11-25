"""Exercise Pi 1.6 public client operations for the base case bindings."""
import base64

from record_user_story.scenarios import Untriggered
from record_user_story.stimuli import png

from .continuation import probe


BASE_CASES = {
    'SDK-CASE-002': 'structured-input',
    'SDK-CASE-005': 'active-input',
    'SDK-CASE-006': 'fixture-tools',
    'SDK-CASE-021': 'structured-input',
    'SDK-CASE-022': 'configuration-timing',
    'SDK-CASE-023': 'structured-input',
    'SDK-CASE-024': 'structured-input',
    'SDK-CASE-027': 'structured-input',
    'SDK-CASE-030': 'interrupt',
    'SDK-CASE-031': 'session-close',
    'SDK-CASE-033': 'history',
    'SDK-CASE-036': 'history',
}


def run(plan, report, flow):
    case = plan['caseId']
    code = case.removeprefix('SDK-CASE-')
    if case not in BASE_CASES:
        raise ValueError(f'no official Pi driver for {case}')
    parameters = flow.definition.get('parameters', {})
    images = [{'type': 'image', 'mimeType': 'image/png', 'data': base64.b64encode(png()).decode()}] if code == '002' else None
    observed = probe(plan, report, 'base.mjs', {
        'caseId': case, 'prompts': flow.definition['prompts'], 'images': images,
        'initialModel': plan.get('initialModel', ''), 'model': plan.get('model', ''),
        'effort': parameters.get('effort'), 'targetEffort': parameters.get('targetEffort'),
        'replay': bool(plan.get('_replay')),
        'config': plan['config'],
        'history': plan.get('historyBindings', []),
    })
    report['sessionId'] = observed['sessionId']
    for turn in observed['turns']:
        flow.submit()
        report['inputs'] += 1
        if code in ('005', '030'):
            action = flow.progress()
            flow.action(action, turn['disposition']['outcome'])
            if code == '005':
                report['inputs'] += 1
        for tool in observed['requests']:
            flow.request(tool)
            report['requests'] += 1
        report['events'] += turn['events']
        report['receipts'].append(turn)
        flow.result()
        report['results'] += 1
    if observed['configuration']:
        report['piConfiguration'] = observed['configuration']
    for key in ('windows', 'sessionClose', 'savedHistory'):
        if key in observed:
            report[key] = observed[key]


def require_wire(frames, report):
    """Verify command/response identity and progress-driven controls in native order."""
    code = report['caseId'].removeprefix('SDK-CASE-')
    pending = {}
    commands = []
    steps = []
    responses = []
    for frame in frames:
        wire = frame['wire']
        kind = wire.get('type')
        if frame['direction'] == 'c2s':
            command_id = wire.get('id')
            if not command_id or command_id in pending:
                raise Untriggered('Pi sent a command without a unique outstanding ID')
            pending[command_id] = wire
            commands.append(kind)
            steps.append(kind)
        elif kind == 'response':
            request = pending.pop(wire.get('id'), None)
            if not request or request['type'] != wire.get('command'):
                raise Untriggered('Pi response did not correlate with a native command')
            responses.append((request, wire))
        elif kind == 'message_update' and wire.get('assistantMessageEvent', {}).get('type') == 'text_delta':
            steps.append('progress')
        elif kind == 'agent_settled':
            steps.append('aborted' if wire.get('aborted') else 'settled')

    if pending or commands[:1] != ['get_state'] or commands[-1:] != ['get_state']:
        raise Untriggered('Pi command sequence did not start and finish with a settled state read')
    for command, response in responses:
        kind = command['type']
        data = response.get('data', {})
        if (kind == 'get_state' and (not response['success'] or data.get('sessionId') != report['sessionId'])
                or kind == 'prompt' and (not response['success'] or data.get('disposition') != 'started')):
            raise Untriggered('Pi state or input acknowledgement differs from the client result')
    controlled = {'005': 'steer', '024': 'set_model', '030': 'abort'}
    if code in controlled:
        action = controlled[code]
        terminal = 'aborted' if code == '030' else 'settled'
        if (steps.count(action) != 1 or terminal not in steps
                or 'progress' not in steps[steps.index('prompt') + 1:steps.index(action)]
                or steps.index(action) > steps.index(terminal)):
            raise Untriggered(f'Pi did not send {action} between active progress and the terminal')
        if code == '030' and any(kind in commands for kind in ('clear_queue', 'abort_bash', 'abort_retry')):
            raise Untriggered('Pi used an additional interrupt control')
    if code == '002':
        prompt = next(command for command, _ in responses if command['type'] == 'prompt')
        if prompt.get('images') != [{'type': 'image', 'mimeType': 'image/png', 'data': base64.b64encode(png()).decode()}]:
            raise Untriggered('Pi did not submit the native image stimulus')
    if code == '036':
        windows = [(command, response) for command, response in responses if command['type'] == 'get_entries']
        expected = report['windows']
        if (len(windows) != 3 or windows[0][0].get('since') is not None
                or windows[1][0].get('since') != expected[0][-1]
                or windows[2][0].get('since') != expected[1][-1]
                or any(not response['success'] for _, response in windows)
                or [[entry['id'] for entry in response['data']['entries']] for _, response in windows] != [*expected, []]):
            raise Untriggered('Pi native entries cursor did not produce the expected windows')
