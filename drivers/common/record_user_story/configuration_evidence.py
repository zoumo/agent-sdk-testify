"""Assert changed models and actual media answers, not transport acknowledgements."""
from .scenarios import Untriggered


def assess(plan, frames, report):
    parameters = plan['scenario'].get('parameters', {})
    kind = parameters.get('inputKind')
    incoming = [f['wire'] for f in frames if f['direction'] == 's2c']
    if kind in ('image', 'mixed', 'document'):
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
        assess_display(frames, report)
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
            if w.get('method') == 'thread/settings/update' and w.get('params', {}).get('model') == target:
                changed = True
            continue
        if not changed:
            continue
        if w.get('method') == 'thread/settings/updated':
            model = w.get('params', {}).get('threadSettings', {}).get('model')
            if model:
                observed.append(model)
    evidence.update(initialModel=initial, targetModel=target, observedModels=observed)
    if not changed or (timing != 'during-execution' and target not in observed):
        raise Untriggered('target model effectiveness is not established by native observations')
    evidence['effectiveScope'] = ('current execution not guaranteed; observed models only' if timing == 'during-execution'
                                  else 'thread-settings model; execution model not independently reported')


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
    assess_codex_effort(frames, report, evidence, initial, target)


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


def assess_display(frames, report):
    """Check native serialization and correlated success, not model display behavior."""
    evidence = report.get('configurationEvidence', {})
    requests = evidence.get('requests', [])
    if not any(r.get('requested', {}).get('summary') == 'none' and r.get('acknowledged') is True
               for r in requests):
        raise Untriggered('display configuration was not acknowledged by the SDK')
    for index, frame in enumerate(frames):
        wire = frame['wire']
        if frame['direction'] != 'c2s':
            continue
        request_id = wire.get('id')
        if wire.get('method') != 'thread/settings/update' or wire.get('params', {}).get('summary') != 'none' or request_id is None:
            continue
        replies = []
        for later in frames[index + 1:]:
            response = later['wire']
            if later['direction'] != 's2c':
                continue
            if response.get('id') == request_id and ('result' in response or 'error' in response):
                replies.append('result' in response and 'error' not in response)
        if replies == [True]:
            evidence['effectiveScope'] = 'native display configuration forwarded and success response handled; visual effect not asserted'
            return
    raise Untriggered('display configuration lacks an exact native request and correlated success response')
