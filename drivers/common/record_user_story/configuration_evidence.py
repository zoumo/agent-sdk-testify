"""Assert changed models and actual media answers, not transport acknowledgements."""
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
