"""Require actual parent/child model observations, separate from requested model."""
from .scenarios import Untriggered


def assess(frames, report, parameters, requested):
    if not (parameters.get('childConfiguration') or parameters.get('childDetailPolicy')):
        return
    wires = [f['wire'] for f in frames if f['direction'] == 's2c']
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
