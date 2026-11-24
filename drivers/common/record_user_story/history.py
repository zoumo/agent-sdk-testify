"""Independent native history queries after the seed execution has ended."""
from .scenarios import Untriggered
from .adapters.common import serial


def pages(request, method, params, *, require_multiple=False):
    """Follow opaque native cursors, retaining server order and exact identities."""
    records, cursors, count = [], set(), 0
    current = dict(params)
    while True:
        page = request(method, current)
        data = page.get('data')
        if not isinstance(data, list):
            raise ValueError('history response has no native data array')
        records.extend(data)
        count += 1
        cursor = page.get('nextCursor')
        if cursor is None:
            break
        if not isinstance(cursor, str) or not cursor or cursor in cursors or not data or count >= 100:
            raise ValueError('history cursor did not make bounded progress')
        cursors.add(cursor)
        current = dict(params, cursor=cursor)
    ids = [record.get('id') for record in records]
    if any(not identity for identity in ids) or len(set(ids)) != len(ids):
        raise ValueError('history page identities are absent or repeated')
    if require_multiple and (count < 2 or len(records) < 2):
        raise Untriggered('native history did not produce a populated second page')
    return records


def codex(client, identity, plan, report, parameters):
    from pydantic import BaseModel, ConfigDict

    class NativeResponse(BaseModel):
        model_config = ConfigDict(extra='allow')

    def request(method, params):
        response = serial(client.request(method, params, response_model=NativeResponse))
        report['receipts'].append({'history-query': {'method': method, 'params': dict(params), 'response': response}})
        return response

    # Detach the setup runtime first. No start/resume/submit is allowed below.
    detached = serial(client.request('thread/unsubscribe', {'threadId': identity}, response_model=NativeResponse))
    report['receipts'].append({'history-setup-ended': {'threadId': identity, 'response': detached}})
    roots = pages(request, 'thread/list', {'cwd': plan['project'], 'sourceKinds': ['cli', 'vscode', 'exec', 'appServer'], 'limit': 10})
    selected = next((item for item in roots if item['id'] == identity), None)
    if selected is None:
        raise Untriggered('seed session not independently discovered in native list')
    target = selected['id']
    if parameters.get('historyTarget') == 'child-messages':
        children = pages(request, 'thread/list', {'parentThreadId': target, 'sourceKinds': ['subAgentThreadSpawn'], 'limit': 10})
        if not children:
            raise Untriggered('no saved native child discovered independently')
        target = children[0]['id']
        if children[0].get('parentThreadId') != selected['id']:
            raise ValueError('discovered child does not identify its native parent')
    metadata = request('thread/read', {'threadId': target, 'includeTurns': False})
    if metadata.get('thread', {}).get('id') != target:
        raise ValueError('history metadata identity differs from discovery')
    if parameters.get('historyTarget') == 'pagination':
        turns = pages(request, 'thread/turns/list', {'threadId': target, 'limit': 1, 'sortDirection': 'asc', 'itemsView': 'full'}, require_multiple=True)
        whole = pages(request, 'thread/turns/list', {'threadId': target, 'limit': 100, 'sortDirection': 'asc', 'itemsView': 'full'})
        if turns != whole:
            raise ValueError('native history pages differ from complete ordered history')
    else:
        history = request('thread/read', {'threadId': target, 'includeTurns': True})
        if history.get('thread', {}).get('id') != target:
            raise ValueError('history content identity differs from discovery')
        turns = history['thread'].get('turns', [])
    if not turns or not any(turn.get('items') for turn in turns):
        raise Untriggered('discovered history has no saved content')
    if any(turn.get('status') != 'completed' for turn in turns):
        raise Untriggered('saved history contains an unsuccessful or active execution')
