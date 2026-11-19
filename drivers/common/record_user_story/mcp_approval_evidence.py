"""Require approval and the successful MCP tool result on the selected Thread."""
from .scenarios import Untriggered


def assess(plan, frames, report):
    root = next(receipt['thread/start']['thread']['id'] for receipt in report['receipts']
                if 'thread/start' in receipt)
    incoming = [(index, frame['wire']) for index, frame in enumerate(frames) if frame['direction'] == 's2c']
    approvals = [(index, wire) for index, wire in incoming
                 if wire.get('method') == 'mcpServer/elicitation/request' and 'id' in wire]
    if len(approvals) != 1:
        raise Untriggered('expected exactly one native MCP approval')
    requested, request = approvals[0]
    thread = request['params'].get('threadId')
    turn = request['params'].get('turnId')
    if not thread or not turn:
        raise Untriggered('MCP approval lacks native thread and turn identity')

    def items(method):
        return [(index, wire['params'], wire['params']['item']) for index, wire in incoming
                if wire.get('method') == method and 'item' in wire.get('params', {})]

    completed = items('item/completed')
    if plan['scenario']['parameters']['approvalThread'] == 'child':
        if thread == root or not any(params.get('threadId') == root and item.get('tool') == 'spawnAgent'
                                     and thread in item.get('receiverThreadIds', [])
                                     for _, params, item in completed):
            raise Untriggered('MCP approval did not originate from a spawned child Thread')
    elif thread != root:
        raise Untriggered('MCP approval did not originate from the root Thread')
    answers = [(index, frame['wire']) for index, frame in enumerate(frames)
               if frame['direction'] == 'c2s' and 'method' not in frame['wire']
               and type(frame['wire'].get('id')) is type(request['id'])
               and frame['wire'].get('id') == request['id']]
    if (len(answers) != 1 or answers[0][0] <= requested
            or answers[0][1].get('result') != {'action': 'accept'}):
        raise Untriggered('MCP approval lacks a correlated accept response')
    calls = [(index, item) for index, params, item in completed
             if params.get('threadId') == thread and params.get('turnId') == turn
             and item.get('type') == 'mcpToolCall' and item.get('server') == 'approval_fixture'
             and item.get('tool') == 'token']
    if len(calls) != 1:
        raise Untriggered('expected one completed MCP call in the approval Thread and turn')
    finished, item = calls[0]
    expected_content = [{'type': 'text', 'text': 'SYNTHETIC_MCP_APPROVED'}]
    if (finished <= answers[0][0] or item.get('status') != 'completed' or item.get('error') is not None
            or (item.get('result') or {}).get('content') != expected_content):
        raise Untriggered('the approved MCP call did not return the synthetic token')
    report['receipts'].append({'mcp-approval': {'threadId': thread, 'turnId': turn,
                                               'requestId': request['id'], 'itemId': item['id'],
                                               'content': expected_content}})
