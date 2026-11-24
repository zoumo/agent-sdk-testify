"""Assess child topology only from native identities, output and timing facts."""
from .scenarios import Untriggered


def codex(client, root_id, report, serial, mode):
    pending, nodes = [root_id], {}
    while pending:
        identity = pending.pop(0)
        if identity in nodes:
            continue
        response = serial(client.thread_read(identity, include_turns=True))
        thread = response['thread']
        if thread['id'] != identity:
            raise ValueError('native child query returned a different identity')
        nodes[identity] = thread
        report['receipts'].append({'topology/thread-read': response})
        for turn in thread.get('turns', []):
            for item in turn.get('items', []):
                children = item.get('receiverThreadIds', []) if item.get('tool') == 'spawnAgent' else []
                if item.get('type') == 'subAgentActivity' and item.get('agentThreadId'):
                    children = [item['agentThreadId']]
                pending.extend(child for child in children if child not in nodes)
    children = [node for identity, node in nodes.items() if identity != root_id]
    def output(node):
        return any(item.get('type') == 'agentMessage' and item.get('text')
                   for turn in node.get('turns', []) for item in turn.get('items', []))
    if any(turn.get('status') == 'failed' for node in children for turn in node.get('turns', [])):
        raise ValueError('native topology child failed')
    if mode == 'nested':
        direct = {node['id'] for node in children if node.get('parentThreadId') == root_id}
        verified = [node for node in children if node.get('parentThreadId') in direct and output(node)]
        if not verified:
            raise Untriggered('no native grandchild parent edge and output established')
    else:
        direct = [node for node in children if node.get('parentThreadId') == root_id and output(node)]
        intervals = [(t.get('startedAt'), t.get('completedAt')) for node in direct for t in node.get('turns', [])]
        overlap = any(a is not None and b is not None and c is not None and d is not None and max(a,c) < min(b,d)
                      for i,(a,b) in enumerate(intervals) for c,d in intervals[i+1:])
        if len(direct) < 2 or not overlap:
            raise Untriggered('two sibling outputs with native execution overlap not established')
    report['topology'] = {'mode': mode, 'rootId': root_id, 'nativeNodeIds': list(nodes), 'verified': True}
