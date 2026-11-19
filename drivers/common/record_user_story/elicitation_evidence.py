"""Correlate each official Claude answer with its original control request."""
from .scenarios import Untriggered


def assess(frames, report):
    requests = [(i, f['wire']) for i, f in enumerate(frames) if f['direction'] == 's2c'
                and f['wire'].get('type') == 'control_request'
                and f['wire'].get('request', {}).get('subtype') == 'elicitation']
    decisions = [r['elicitation'] for r in report['receipts'] if 'elicitation' in r]
    if len(requests) != 6 or len(decisions) != 6 or len({r.get('request_id') for _, r in requests}) != 6:
        raise Untriggered('expected six distinct native elicitation requests')
    after = -1
    for (index, request), decision in zip(requests, decisions):
        request_id = request.get('request_id')
        native = request['request']
        replies = [(i, f['wire']['response']) for i, f in enumerate(frames) if f['direction'] == 'c2s'
                   and f['wire'].get('type') == 'control_response'
                   and f['wire'].get('response', {}).get('request_id') == request_id]
        if (not request_id or native.get('mcp_server_name') != 'elicitation_fixture'
                or native.get('mode') != decision['request']['mode']
                or native.get('message') != decision['request']['message']
                or len(replies) != 1 or not after < index < replies[0][0]
                or replies[0][1].get('subtype') != 'success'
                or replies[0][1].get('response') != decision['answer']):
            raise Untriggered('standalone elicitation lacks a correlated successful native response')
        after = replies[0][0]
    token = {'type': 'text', 'text': 'SYNTHETIC_ELICITATION_SEQUENCE_DONE'}
    def tool_result(frame):
        items = frame['wire'].get('message', {}).get('content', [])
        return frame['direction'] == 's2c' and frame['wire'].get('type') == 'user' and any(
            isinstance(item, dict) and item.get('type') == 'tool_result' and item.get('content') == [token]
            for item in items if isinstance(items, list))
    if not any(tool_result(f) for f in frames[after + 1:]):
        raise Untriggered('Claude MCP fixture did not observe all elicitation answers')
