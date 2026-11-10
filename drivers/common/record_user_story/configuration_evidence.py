"""Assert changed models and actual media answers, not transport acknowledgements."""
import json
from pathlib import Path

from .adapters.common import effort_keeps_ultracode
from .scenarios import Untriggered


def assess(plan, frames, report):
    parameters = plan['scenario'].get('parameters', {})
    kind = parameters.get('inputKind')
    incoming = [f['wire'] for f in frames if f['direction'] == 's2c']
    if kind in ('image', 'mixed', 'document'):
        if plan['provider'] == 'claude':
            text = '\n'.join(w.get('result', '') or '' for w in incoming if w.get('type') == 'result')
        else:
            text = '\n'.join(w.get('params', {}).get('item', {}).get('text', '') for w in incoming
                             if w.get('method') == 'item/completed'
                             and w.get('params', {}).get('item', {}).get('type') == 'agentMessage')
        expected = 'synthetic document 42' if kind == 'document' else 'blue'
        if text.strip().rstrip('.').lower() != expected:
            raise Untriggered('native answer does not establish supplied media content: ' + expected)
        report['receipts'].append({'media-content-observed': {'kind': kind, 'expected': expected}})
    configuration = plan['scenario'].get('configuration', {})
    timing = parameters.get('configurationTiming') or configuration.get('timing')
    setting = parameters.get('setting') or ('model' if configuration.get('modelSource') else None)
    if setting == 'thinking' and timing == 'before-first-input':
        # The native control acknowledgement provides no budget readback or
        # application scope. Model identity, effort and usage cannot supply it.
        report.setdefault('configurationEvidence', {})['effectiveScope'] = 'unknown'
        raise Untriggered('native thinking budget effect is not established by protocol observations')
    if setting == 'display':
        assess_display(plan, frames, report)
        return
    if setting == 'effort':
        assess_effort(plan, frames, report)
        return
    if setting != 'model' or timing not in ('before-first-input', 'between-inputs', 'during-execution'):
        return
    initial, target = plan.get('initialModel'), plan.get('model')
    if not initial or not target or initial == target:
        raise Untriggered('model change requires distinct explicit initial and target models')
    evidence = report.get('configurationEvidence', {})
    requests = evidence.get('requests', [])
    if not any(r['requested'].get('model') == target and r.get('acknowledged') for r in requests):
        raise Untriggered('different target model was not acknowledged')
    observed = []
    changed = False
    for f in frames:
        w = f['wire']
        if f['direction'] == 'c2s':
            request = w.get('request', {}) if plan['provider'] == 'claude' else w.get('params', {})
            if request.get('model') == target and (w.get('type') == 'control_request'
                                                   or w.get('method') == 'thread/settings/update'):
                changed = True
            continue
        if not changed:
            continue
        if plan['provider'] == 'claude' and w.get('type') == 'assistant' and not w.get('parent_tool_use_id'):
            model = w.get('message', {}).get('model')
            if model:
                observed.append(model)
        elif plan['provider'] == 'codex' and w.get('method') == 'thread/settings/updated':
            model = w.get('params', {}).get('threadSettings', {}).get('model')
            if model:
                observed.append(model)
    evidence.update(initialModel=initial, targetModel=target, observedModels=observed)
    # The theta gateway returns the unprefixed upstream model in Claude messages.
    # Preserve both original identities; do not substitute a different model.
    matches = {target}
    if plan['provider'] == 'claude' and target.startswith('theta/'):
        matches.add(target.removeprefix('theta/'))
    if not changed or (timing != 'during-execution' and not matches.intersection(observed)):
        raise Untriggered('target model effectiveness is not established by native observations')
    evidence['effectiveScope'] = ('current execution not guaranteed; observed models only' if timing == 'during-execution'
                                  else ('assistant-message model after change; theta prefix mapping only'
                                        if plan['provider'] == 'claude' else 'thread-settings model; execution model not independently reported'))


def _claude_control_response(frames, index, request):
    request_id = request.get('request_id')
    for later in range(index + 1, len(frames)):
        frame = frames[later]
        wire = frame['wire']
        if frame['direction'] != 's2c' or wire.get('type') != 'control_response':
            continue
        body = wire.get('response', {})
        if body.get('request_id') == request_id:
            return later, body
    return None, None


def assess_effort(plan, frames, report):
    """Require creation A, runtime request B, and a later native observation."""
    parameters = plan['scenario'].get('parameters', {})
    initial, target = parameters.get('effort'), parameters.get('targetEffort')
    if (parameters.get('configurationTiming') != 'between-inputs'
            or not isinstance(initial, str) or not initial
            or not isinstance(target, str) or not target or initial == target):
        raise Untriggered('effort change requires distinct creation and runtime values')
    evidence = report.setdefault('configurationEvidence', {
        'requests': [], 'observed': [], 'effectiveScope': 'unknown'})
    if plan['provider'] == 'claude':
        assess_claude_effort(plan, frames, report, evidence, initial, target)
    elif plan['provider'] == 'codex':
        assess_codex_effort(frames, report, evidence, initial, target)
    else:
        raise Untriggered('effort evidence is unsupported for provider')


def assess_claude_effort(plan, frames, report, evidence, initial, target):
    args = plan.get('args') or []
    if plan.get('_agentResults') or plan.get('root'):
        from .environment import capture_root
        source = capture_root(plan) / 'plan.json'
        if source.exists():
            recorded = json.loads(source.read_bytes())
            if (recorded.get('provider') != plan['provider']
                    or recorded.get('caseId') != plan['scenario']['case']['id']):
                raise Untriggered('recorded launch plan has a different case identity')
            args = recorded.get('args') or []
    creation = []
    for index, value in enumerate(args):
        if value == '--effort':
            creation.append(args[index + 1] if index + 1 < len(args) else None)
        elif value.startswith('--effort='):
            creation.append(value.split('=', 1)[1])
    if creation != [initial]:
        raise Untriggered('Claude creation effort is not the configured --effort value')
    inputs = [(index, frame['wire']) for index, frame in enumerate(frames)
              if frame['direction'] == 'c2s' and frame['wire'].get('type') == 'user'
              and frame['wire'].get('parent_tool_use_id') is None]
    results = [(index, frame['wire']) for index, frame in enumerate(frames)
               if frame['direction'] == 's2c' and frame['wire'].get('type') == 'result'
               and frame['wire'].get('parent_tool_use_id') is None]
    if len(inputs) != 2 or len(results) != 2:
        raise Untriggered('effort change requires two root inputs and results')
    settings = [(index, frame['wire']) for index, frame in enumerate(frames)
                if frame['direction'] == 'c2s' and frame['wire'].get('type') == 'control_request'
                and frame['wire'].get('request', {}).get('subtype') == 'get_settings']
    if len(settings) != 1:
        raise Untriggered('runtime effort requires exactly one get_settings preflight')
    preflight_request_at, settings_request = settings[0]
    preflight_response_at, settings_response = _claude_control_response(
        frames, preflight_request_at, settings_request)
    if preflight_response_at is None or settings_response.get('subtype') != 'success':
        raise Untriggered('runtime effort preflight was not successful')
    preflight = settings_response.get('response', {}).get('applied', {})
    if preflight.get('effort') != initial:
        raise Untriggered('runtime effort preflight does not report the configured creation effort')
    current_ultracode = preflight.get('ultracode')
    if not isinstance(current_ultracode, bool):
        raise Untriggered('runtime effort preflight does not report applied.ultracode')
    applies = [(index, frame['wire']) for index, frame in enumerate(frames)
               if index > preflight_response_at and frame['direction'] == 'c2s'
               and frame['wire'].get('type') == 'control_request'
               and frame['wire'].get('request', {}).get('subtype') == 'apply_flag_settings']
    if len(applies) != 1:
        raise Untriggered('runtime effort requires exactly one apply_flag_settings request after preflight')
    apply_at, apply = applies[0]
    requested_settings = apply.get('request', {}).get('settings', {})
    expected_ultracode = effort_keeps_ultracode(target, current_ultracode)
    if requested_settings != {'effortLevel': target, 'ultracode': expected_ultracode}:
        raise Untriggered('runtime effort request does not match the target and preflight state')
    ack_at, ack = _claude_control_response(frames, apply_at, apply)
    if ack_at is None or ack.get('subtype') != 'success':
        raise Untriggered('runtime effort request was not acknowledged')
    if any(frame['direction'] == 'c2s' and frame['wire'].get('type') == 'control_request'
           and frame['wire'].get('request', {}).get('subtype') == 'set_max_thinking_tokens'
           for frame in frames):
        raise Untriggered('effort must not be represented as a thinking-token budget')
    if not (inputs[0][0] < results[0][0] < preflight_request_at < preflight_response_at
            < apply_at < ack_at < inputs[1][0] < results[1][0]):
        raise Untriggered('effort preflight and update are not ordered between the two executions')
    evidence.update(initialEffort=initial, targetEffort=target,
                    creationOperation='claude --effort', runtimeOperation='apply_flag_settings',
                    preflightEffort=preflight.get('effort'),
                    preflightUltracode=current_ultracode,
                    effectiveScope='creation argument and applied effort observed before the update; '
                                    'runtime update acknowledged after native preflight; '
                                    'post-update effort and per-turn use not independently reported')
    report.setdefault('receipts', []).append({'effort-preflight': {
        'initial': initial, 'target': target, 'source': 'get_settings.applied.ultracode',
        'effort': preflight.get('effort'), 'ultracode': current_ultracode}})


def _codex_response(frames, index, request):
    request_id = request.get('id')
    for later in range(index + 1, len(frames)):
        frame = frames[later]
        wire = frame['wire']
        if frame['direction'] == 's2c' and wire.get('id') == request_id:
            return later, wire
    return None, None


def assess_codex_effort(frames, report, evidence, initial, target):
    starts = [(index, frame['wire']) for index, frame in enumerate(frames)
              if frame['direction'] == 'c2s' and frame['wire'].get('method') == 'thread/start']
    if len(starts) != 1:
        raise Untriggered('effort change requires exactly one native thread/start')
    start_at, start = starts[0]
    config = start.get('params', {}).get('config', {})
    if config.get('model_reasoning_effort') != initial:
        raise Untriggered('thread/start does not carry the configured creation effort')
    start_response_at, start_response = _codex_response(frames, start_at, start)
    if start_response_at is None:
        raise Untriggered('thread/start effort request lacks a response')
    thread = start_response.get('result', {}).get('thread', {})
    thread_id = thread.get('id')
    if not thread_id:
        raise Untriggered('thread/start response has no thread identity')
    creation_observed = thread.get('reasoningEffort')
    if creation_observed is not None and creation_observed != initial:
        raise Untriggered('thread/start response reports a different creation effort')
    turns = []
    for index, request in [(index, frame['wire']) for index, frame in enumerate(frames)
                           if frame['direction'] == 'c2s'
                           and frame['wire'].get('method') == 'turn/start'
                           and frame['wire'].get('params', {}).get('threadId') == thread_id]:
        response_at, response = _codex_response(frames, index, request)
        turn_id = response.get('result', {}).get('turn', {}).get('id') if response else None
        if response_at is None or not turn_id:
            raise Untriggered('turn/start effort execution lacks a turn identity')
        completed = [(at, frame['wire']) for at, frame in enumerate(frames)
                     if at > response_at and frame['direction'] == 's2c'
                     and frame['wire'].get('method') == 'turn/completed'
                     and frame['wire'].get('params', {}).get('turn', {}).get('id') == turn_id]
        if len(completed) != 1:
            raise Untriggered('turn/start effort execution lacks one completion')
        turns.append({'start': index, 'response': response_at, 'completed': completed[0][0]})
    if len(turns) != 2:
        raise Untriggered('effort change requires two root executions')
    updates = [(index, frame['wire']) for index, frame in enumerate(frames)
               if frame['direction'] == 'c2s' and frame['wire'].get('method') == 'thread/settings/update'
               and frame['wire'].get('params', {}).get('threadId') == thread_id
               and frame['wire'].get('params', {}).get('effort') == target
               and index > turns[0]['completed']]
    if len(updates) != 1:
        raise Untriggered('runtime effort requires one thread/settings/update after the first result')
    update_at, update = updates[0]
    ack_at, ack = _codex_response(frames, update_at, update)
    if ack_at is None or 'result' not in ack or 'error' in ack:
        raise Untriggered('runtime effort update was not acknowledged')
    observed = [(index, frame['wire']) for index, frame in enumerate(frames)
                if ack_at < index < turns[1]['completed'] and frame['direction'] == 's2c'
                and frame['wire'].get('method') == 'thread/settings/updated'
                and frame['wire'].get('params', {}).get('threadId') == thread_id
                and frame['wire'].get('params', {}).get('threadSettings', {}).get('effort') == target]
    if not observed:
        raise Untriggered('runtime effort lacks a later native thread/settings observation')
    observed_at, observed_wire = observed[0]
    if not (turns[0]['start'] < turns[0]['completed'] < update_at < ack_at < observed_at
            < turns[1]['completed'] and turns[0]['completed'] < turns[1]['start']):
        raise Untriggered('runtime effort observation is not ordered with the two executions')
    evidence.update(initialEffort=initial, targetEffort=target,
                    creationOperation='thread/start config', runtimeOperation='thread/settings/update',
                    observedEffort=observed_wire.get('params', {}).get('threadSettings', {}).get('effort'),
                    effectiveScope='creation thread config; runtime thread settings observed after acknowledgement; '
                                    'per-turn execution use not independently reported')
    report.setdefault('receipts', []).append({'effort-observation': {
        'initial': initial, 'target': target, 'source': 'thread/settings/updated',
        'effective': observed_wire.get('params', {}).get('threadSettings', {}).get('effort'),
        'creationObserved': creation_observed}})


def assess_display(plan, frames, report):
    """Check native serialization and correlated success, not model display behavior."""
    claude = plan['provider'] == 'claude'
    field, expected = ('thinking_display', 'omitted') if claude else ('summary', 'none')
    evidence = report.get('configurationEvidence', {})
    requests = evidence.get('requests', [])
    if not any(r.get('requested', {}).get(field) == expected and r.get('acknowledged') is True
               for r in requests):
        raise Untriggered('display configuration was not acknowledged by the SDK')
    for index, frame in enumerate(frames):
        wire = frame['wire']
        if frame['direction'] != 'c2s':
            continue
        request = wire.get('request', {}) if claude else wire.get('params', {})
        operation = (wire.get('type') == 'control_request'
                     and request.get('subtype') == 'set_max_thinking_tokens') if claude else (
                         wire.get('method') == 'thread/settings/update')
        request_id = wire.get('request_id' if claude else 'id')
        if not operation or request.get(field) != expected or request_id is None:
            continue
        replies = []
        for later in frames[index + 1:]:
            response = later['wire']
            if later['direction'] != 's2c':
                continue
            if claude:
                body = response.get('response', {})
                if response.get('type') == 'control_response' and body.get('request_id') == request_id:
                    replies.append(body.get('subtype') == 'success')
            elif response.get('id') == request_id and ('result' in response or 'error' in response):
                replies.append('result' in response and 'error' not in response)
        if replies == [True]:
            evidence['effectiveScope'] = 'native display configuration forwarded and success response handled; visual effect not asserted'
            return
    raise Untriggered('display configuration lacks an exact native request and correlated success response')
