"""Require actual parent/child model observations, separate from requested model."""
from .scenarios import Untriggered


def assess(provider, frames, report, parameters, requested):
    if not (parameters.get('childConfiguration') or parameters.get('childDetailPolicy')):
        return
    wires = [f['wire'] for f in frames if f['direction'] == 's2c']
    if provider == 'codex':
        start = next((r['thread/start'] for r in report['receipts'] if 'thread/start' in r), {})
        root = start.get('thread', {}).get('id')
        children = [r['history-query']['response']['thread'] for r in report['receipts']
                    if r.get('history-query', {}).get('method') == 'thread/read'
                    and r['history-query']['response'].get('thread', {}).get('parentThreadId') == root]
        ids = {c['id'] for c in children}
        root_model = start.get('model')
        models = {c['id']: c.get('model') for c in children}
        details = [w for w in wires if w.get('method') == 'item/agentMessage/delta'
                   and w.get('params', {}).get('threadId') in ids and w['params'].get('delta')]
        if parameters.get('childConfiguration') and root_model != requested:
            raise ValueError('root model differs from explicit creation configuration')
    else:
        root = next((w.get('session_id') for w in wires if w.get('type') == 'result'), None)
        agents = [r['list_subagents'] for r in report['receipts'] if 'list_subagents' in r]
        ids = {x for r in agents if r.get('sessionId') == root for x in r.get('agentIds', [])}
        calls = {b.get('id') for w in wires if w.get('type') == 'assistant' and w.get('session_id') == root
                 and not w.get('parent_tool_use_id') for b in w.get('message', {}).get('content', [])
                 if b.get('type') == 'tool_use' and b.get('name') == 'Agent' and b.get('input', {}).get('model') in (None, 'inherit')}
        task_ids = {w.get('task_id') for w in wires if w.get('subtype') == 'task_started'
                    and w.get('session_id') == root and w.get('tool_use_id') in calls}
        if not ids or not ids.issubset(task_ids):
            raise Untriggered('saved child identity is not linked to a native inherited Agent invocation')
        parent_models = {w['message']['model'] for w in wires if w.get('type') == 'assistant'
                         and w.get('session_id') == root and not w.get('parent_tool_use_id') and w.get('message', {}).get('model')}
        root_model = next(iter(parent_models)) if len(parent_models) == 1 else None
        models = {w.get('parent_tool_use_id'): w.get('message', {}).get('model') for w in wires
                  if w.get('type') == 'assistant' and w.get('session_id') == root and w.get('parent_tool_use_id') in calls}
        details = [w for w in wires if w.get('type') == 'assistant' and w.get('parent_tool_use_id') in calls
                   and any(b.get('type') == 'text' and b.get('text') for b in w.get('message', {}).get('content', []))]
    if not root or not ids or not details:
        raise Untriggered('native child identity and output are not established')
    if parameters.get('childConfiguration'):
        if not requested or not root_model or not models or any(m != root_model for m in models.values()):
            raise Untriggered('actual child model inheritance is not established')
        report['receipts'].append({'child-model-inheritance': {'requested': requested, 'rootId': root,
                                  'rootObservedModel': root_model, 'childObservedModels': models,
                                  'scope': 'model only; no reasoning inheritance claim'}})
    if parameters.get('childDetailPolicy'):
        report['receipts'].append({'child-detail-selection': {'requested': parameters['childDetailPolicy'],
                                  'rootId': root, 'childIds': sorted(ids), 'nativeDetailCount': len(details),
                                  'scope': 'native source; Go SDK delivery is checked by SDK simulation'}})
