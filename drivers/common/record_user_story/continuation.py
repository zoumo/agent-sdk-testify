"""Evidence for two executions on one continuously owned native thread."""
from .scenarios import Untriggered


def require_codex_continuation(frames, report):
    def require(condition):
        if not condition:
            raise Untriggered('continuous session lacks correlated submissions and successful results')

    receipts = report['receipts']
    opened = [x['thread/start'] for x in receipts if 'thread/start' in x]
    submitted = [x['input-submitted'] for x in receipts if 'input-submitted' in x]
    completed = [x['turn/completed'] for x in receipts if 'turn/completed' in x]
    require(len(opened) == 1 and len(submitted) == len(completed) == 2)
    thread = opened[0].get('thread', {}).get('id')
    turns = [x.get('turn', {}).get('id') for x in submitted]
    require(thread and all(turns) and turns[0] != turns[1])
    require(all(f.get('epoch', 1) == 1 for f in frames))

    def select(direction, method):
        return [(i, f['wire']) for i, f in enumerate(frames)
                if f['direction'] == direction and f['wire'].get('method') == method]

    opens = select('c2s', 'thread/start')
    starts = select('c2s', 'turn/start')
    ends = select('s2c', 'turn/completed')
    require(len(opens) == 1 and len(starts) == len(ends) == 2)
    require(len(select('c2s', 'initialize')) == 1)
    require(opens[0][0] < starts[0][0] < ends[0][0] < starts[1][0] < ends[1][0])
    # Reopening, resuming or detaching cannot stand in for continued observation.
    require(not any(f['direction'] == 'c2s' and f['wire'].get('method') in
                    ('thread/resume', 'thread/unsubscribe', 'thread/archive')
                    for f in frames[:ends[1][0] + 1]))

    def reply(request, after, before):
        require('id' in request)
        found = [(i, f['wire']) for i, f in enumerate(frames)
                 if f['direction'] == 's2c' and 'id' in f['wire']
                 and type(f['wire']['id']) is type(request['id']) and f['wire']['id'] == request['id']]
        require(len(found) == 1)
        index, value = found[0]
        require(after < index < before and 'result' in value and 'error' not in value)
        return value['result']

    require(reply(opens[0][1], opens[0][0], starts[0][0]).get('thread', {}).get('id') == thread)
    for n, ((start, request), (end, terminal)) in enumerate(zip(starts, ends)):
        require(request.get('params', {}).get('threadId') == thread)
        boundary = starts[1][0] if n == 0 else len(frames)
        accepted = reply(request, start, boundary)
        require(accepted.get('turn', {}).get('id') == turns[n])
        for value in (terminal.get('params', {}), completed[n]):
            turn = value.get('turn', {})
            require(value.get('threadId') == thread and turn.get('id') == turns[n]
                    and turn.get('status') == 'completed' and turn.get('error') is None)
