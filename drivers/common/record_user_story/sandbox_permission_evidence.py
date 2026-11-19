"""Constrain grants to synthetic directories and correlate each native decision."""
from pathlib import Path

from .scenarios import Untriggered


def answer(project, request, scope):
    permissions = request.get('permissions') or {}
    filesystem = permissions.get('fileSystem') or {}
    target = str(Path(project) / f'grant-{scope}')
    entries = [{'path': {'type': 'path', 'path': target}, 'access': 'write'}]
    if (not request.get('itemId') or request.get('cwd') != project
            or permissions.get('network') is not None
            or set(permissions) - {'fileSystem', 'network'}
            or set(filesystem) - {'read', 'write', 'entries', 'globScanMaxDepth'}
            or filesystem.get('globScanMaxDepth') is not None or filesystem.get('read') not in (None, [])
            or filesystem.get('write') not in (None, [target])
            or filesystem.get('entries') not in (None, entries)
            or (filesystem.get('write') is None and filesystem.get('entries') is None)):
        raise ValueError(f'unexpected filesystem grant for {scope}')
    if scope == 'deny':
        return {'permissions': {}, 'scope': 'turn'}
    return {'permissions': permissions, 'scope': scope}


def assess(plan, frames, report):
    requests = [(index, frame['wire']) for index, frame in enumerate(frames)
                if frame['direction'] == 's2c'
                and frame['wire'].get('method') == 'item/permissions/requestApproval'
                and 'id' in frame['wire']]
    scopes = plan['scenario']['parameters']['approvalScopes']
    turns = [r['input-submitted']['turn']['id'] for r in report['receipts'] if 'input-submitted' in r]
    root = next(r['thread/start']['thread']['id'] for r in report['receipts'] if 'thread/start' in r)
    if len(requests) != len(scopes) or len(turns) != len(scopes):
        raise Untriggered('expected one native permission request per decision and turn')
    for (requested, request), turn, scope in zip(requests, turns, scopes):
        params = request['params']
        replies = [(index, f['wire']) for index, f in enumerate(frames)
                   if f['direction'] == 'c2s' and 'method' not in f['wire']
                   and type(f['wire'].get('id')) is type(request['id']) and f['wire'].get('id') == request['id']]
        result = answer(plan['project'], params, scope)
        resolved = [index for index, f in enumerate(frames) if f['direction'] == 's2c'
                    and f['wire'].get('method') == 'serverRequest/resolved'
                    and f['wire'].get('params', {}).get('requestId') == request['id']
                    and f['wire']['params'].get('threadId') == root]
        completed = [index for index, f in enumerate(frames) if f['direction'] == 's2c'
                     and f['wire'].get('method') == 'turn/completed'
                     and f['wire'].get('params', {}).get('threadId') == root
                     and f['wire']['params'].get('turn', {}).get('id') == turn
                     and f['wire']['params']['turn'].get('status') == 'completed']
        if (params.get('threadId') != root or params.get('turnId') != turn or len(replies) != 1
                or replies[0][1].get('result') != result or len(resolved) != 1 or len(completed) != 1
                or not requested < replies[0][0] < resolved[0] < completed[0]):
            raise Untriggered(f'permission decision {scope} lacks correlated resolution and turn result')
        report['receipts'].append({'sandbox-approval': {'threadId': root, 'turnId': turn,
                                                       'requestId': request['id'], 'response': result}})
