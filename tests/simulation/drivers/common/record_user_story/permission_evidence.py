"""Correlate a held native approval, its controls, and the observed session event."""
from .scenarios import Untriggered


def require_held_permission(frames, report):
    receipts = report['receipts']
    control = next((x['held-permission-control'] for x in receipts if 'held-permission-control' in x), {})
    observed = next((x['held-permission-event'].get('native', {}) for x in receipts if 'held-permission-event' in x), {})
    restore = next((x['held-permission-restore'] for x in receipts if 'held-permission-restore' in x), {})
    result = next((x['result'] for x in receipts if 'result' in x), {})
    event = observed.get('data', observed)
    session = result.get('session_id')
    if (not session or not event.get('uuid') or event.get('session_id') != session
            or observed.get('subtype') != 'status' or event.get('parent_tool_use_id') is not None
            or event.get('permissionMode') != control.get('requested')
            or control.get('acknowledged') is not True or control.get('operation') != 'set_permission_mode'
            or not control.get('previousMode') or not control.get('requested')
            or control['previousMode'] == control['requested']
            or restore.get('acknowledged') is not True or restore.get('mode') != control['previousMode']
            or not result.get('uuid') or result.get('is_error') is not False):
        raise Untriggered('held permission lacks correlated SDK control and session event receipts')

    def locate(direction, predicate, after=-1, before=None):
        for index in range(after + 1, len(frames) if before is None else before):
            frame = frames[index]
            if frame['direction'] == direction and predicate(frame['wire']):
                return index, frame['wire']
        raise Untriggered('held permission lacks native request/control/event/restore/result ordering')

    initial, _ = locate('s2c', lambda w: w.get('type') == 'system' and w.get('subtype') == 'init'
                        and w.get('session_id') == session and w.get('permissionMode') == control.get('previousMode'))
    requested, permission = locate('s2c', lambda w: w.get('type') == 'control_request'
                                   and w.get('request', {}).get('subtype') == 'can_use_tool'
                                   and w.get('request', {}).get('tool_name') == 'Read'
                                   and bool(w.get('request_id')), after=initial)
    tool_id = permission['request'].get('tool_use_id')
    if not tool_id:
        raise Untriggered('held permission lacks its native tool identity')
    locate('s2c', lambda w: w.get('type') == 'assistant' and w.get('session_id') == session
           and w.get('parent_tool_use_id') is None
           and any(item.get('type') == 'tool_use' and item.get('id') == tool_id and item.get('name') == 'Read'
                   for item in w.get('message', {}).get('content', [])), after=initial, before=requested)
    requests = [f for f in frames if f['direction'] == 's2c' and f['wire'].get('type') == 'control_request'
                and f['wire'].get('request_id') == permission['request_id']]
    answers = [(i, f['wire']['response']) for i, f in enumerate(frames)
               if f['direction'] == 'c2s' and f['wire'].get('type') == 'control_response'
               and f['wire'].get('response', {}).get('request_id') == permission['request_id']]
    if (len(requests) != 1 or len(answers) != 1 or answers[0][0] <= requested
            or answers[0][1].get('subtype') != 'success'
            or answers[0][1].get('response', {}).get('behavior') != 'allow'):
        raise Untriggered('held permission requires exactly one original request and allow answer')
    answered = answers[0][0]
    terminal, native_result = locate('s2c', lambda w: w.get('type') == 'result'
                                     and w.get('session_id') == session, after=answered)
    if (native_result.get('is_error') is not False or native_result.get('uuid') != result['uuid']
            or native_result.get('parent_tool_use_id') is not None):
        raise Untriggered('held permission terminal differs from the observed SDK result')
    if any(f['direction'] == 's2c' and f['wire'].get('type') == 'control_cancel_request'
           and f['wire'].get('request_id') == permission['request_id'] for f in frames[requested + 1:terminal]):
        raise Untriggered('held permission was cancelled before its terminal result')

    def mode_request(mode, after):
        return locate('c2s', lambda w: w.get('type') == 'control_request'
                      and w.get('request', {}).get('subtype') == 'set_permission_mode'
                      and w['request'].get('mode') == mode and bool(w.get('request_id')),
                      after=after, before=answered)

    def acknowledged(request, after):
        replies = [(i, f['wire']['response']) for i, f in enumerate(frames)
                   if after < i < answered and f['direction'] == 's2c'
                   and f['wire'].get('type') == 'control_response'
                   and f['wire'].get('response', {}).get('request_id') == request['request_id']]
        if len(replies) != 1 or replies[0][1].get('subtype') != 'success':
            raise Untriggered('held permission control lacks one successful native acknowledgement')
        acknowledged_mode = replies[0][1].get('response', {}).get('mode')
        if acknowledged_mode is not None and acknowledged_mode != request['request']['mode']:
            raise Untriggered('held permission control acknowledged a different native mode')
        return replies[0][0]

    changed, request = mode_request(control['requested'], requested)
    confirmed = acknowledged(request, changed)
    delivered, _ = locate('s2c', lambda w: w.get('type') == 'system' and w.get('subtype') == 'status'
                          and w.get('uuid') == event['uuid'] and w.get('session_id') == session
                          and w.get('parent_tool_use_id') is None and w.get('permissionMode') == control['requested'],
                          after=changed, before=answered)
    restored, request = mode_request(control['previousMode'], max(confirmed, delivered))
    acknowledged(request, restored)
