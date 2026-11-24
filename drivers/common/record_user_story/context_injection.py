"""Require native context-only input, ordering, and subsequent model use."""
from .scenarios import Untriggered


def assess(plan, frames, report):
    parameters = plan['scenario']['parameters']
    context_value = parameters['contextValue']
    timing = parameters['injectionTiming']
    outgoing = [(index, frame['wire']) for index, frame in enumerate(frames)
                if frame['direction'] == 'c2s']
    incoming = [(index, frame['wire']) for index, frame in enumerate(frames)
                if frame['direction'] == 's2c']
    injected = [(index, native) for index, native in outgoing
                if native.get('method') == 'thread/inject_items']
    queries = [(index, native) for index, native in outgoing if native.get('method') == 'turn/start']
    if len(injected) != 1 or len(queries) != 2:
        raise ValueError('context injection requires one inject and two explicit queries')
    inject_at, injection = injected[0]
    params = injection.get('params', {})
    root = params.get('threadId')
    expected = [{'type': 'message', 'role': 'user', 'content': [
        {'type': 'input_text', 'text': 'The stored vault code is ' + context_value + '.'}]}]
    acks = [(index, native) for index, native in incoming if native.get('id') == injection.get('id')]
    starts = [(index, native) for index, native in incoming
              if native.get('method') == 'turn/started' and native.get('params', {}).get('threadId') == root]
    results = [(index, native) for index, native in incoming
               if native.get('method') == 'turn/completed' and native.get('params', {}).get('threadId') == root]
    if (not root or set(params) != {'threadId', 'items'} or params['items'] != expected
            or len(acks) != 1 or acks[0][1].get('result') != {} or 'error' in acks[0][1]
            or not inject_at < acks[0][0] < queries[1][0]
            or any(native.get('params', {}).get('threadId') != root for _, native in queries)
            or len(starts) != 2 or len(results) != 2):
        raise ValueError('native inject identity, empty acknowledgement, or turn count differs')
    first_turn = starts[0][1]['params']['turn']['id']
    second_turn = starts[1][1]['params']['turn']['id']
    if (first_turn == second_turn
            or [native['params']['turn']['id'] for _, native in results] != [first_turn, second_turn]
            or any(native['params']['turn']['status'] != 'completed' for _, native in results)):
        raise ValueError('context injection query identities or completion differ')
    outputs = []
    for turn_id in (first_turn, second_turn):
        outputs.append(''.join(native.get('params', {}).get('item', {}).get('text', '')
                               for _, native in incoming if native.get('method') == 'item/completed'
                               and native.get('params', {}).get('threadId') == root
                               and native.get('params', {}).get('turnId') == turn_id
                               and native.get('params', {}).get('item', {}).get('type') == 'agentMessage'))
    activity = [index for index, native in incoming
                if native.get('params', {}).get('threadId') == root
                and (native.get('method') == 'turn/started'
                     or native.get('method') == 'item/agentMessage/delta'
                     or (native.get('method') in ('item/started', 'item/completed')
                         and native.get('params', {}).get('item', {}).get('type') == 'agentMessage'))]
    progress = [index for index, native in incoming
                if native.get('method') == 'item/started'
                and native.get('params', {}).get('threadId') == root
                and native.get('params', {}).get('turnId') == first_turn
                and native.get('params', {}).get('item', {}).get('type') not in (None, 'userMessage')]
    if any(context_value in str(native) for _, native in outgoing if native is not injection):
        raise ValueError('context fact leaked into a non-injection client message')
    if not queries[0][0] < results[0][0] < queries[1][0] < results[1][0]:
        raise ValueError('explicit query and result boundaries differ')
    if timing == 'idle':
        if not results[0][0] < inject_at < queries[1][0]:
            raise ValueError('idle injection did not follow the first result')
        if any(inject_at < index < queries[1][0] for index in activity):
            raise ValueError('idle injection triggered native assistant activity')
    elif (not queries[0][0] < inject_at < results[0][0]
          or not any(queries[0][0] < index < inject_at for index in progress)):
        raise Untriggered('running injection lacks an active native progress boundary')
    if outputs[1].strip() != context_value:
        raise ValueError('subsequent query did not return the exact injected random fact')
    report['contextInjection'] = {
        'timing': timing, 'nativeInputOrdinal': inject_at + 1,
        'explicitQueries': 2, 'extraTurnObserved': False,
        'currentQueryUsedFact': context_value in outputs[0],
        'subsequentQueryUsedFact': True, 'outputs': outputs}
