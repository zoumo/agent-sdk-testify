"""Resume an independently saved Codex parent, then discover existing children."""
import json
from pathlib import Path

from record_user_story.adapters.common import proxy_command, serial
from record_user_story.environment import capture_root, read_regular, recording_temp_root
from record_user_story.existing_children import sha, project_files, verify_seed
from record_user_story.history import pages



def require_wire(plan, frames):
    requests = [f['wire'] for f in frames if f['direction'] == 'c2s']
    methods = [r.get('method') for r in requests]
    if (methods[:3] != ['initialize', 'initialized', 'thread/resume']
            or methods[-1:] != ['thread/unsubscribe'] or not methods[3:-1]
            or any(m != 'thread/list' for m in methods[3:-1])):
        raise ValueError('existing observation must resume once, query, and detach without new input')
    responses = {json.dumps(f['wire']['id']): f['wire'].get('result') for f in frames
                 if f['direction'] == 's2c' and 'id' in f['wire']}
    # Every dependent operation must follow its own successful response on the
    # original wire. SDK receipt order cannot substitute for native causality.
    positions = [(i, f['wire']) for i, f in enumerate(frames) if f['direction'] == 'c2s'
                 and f['wire'].get('method') != 'initialized']
    ids = [json.dumps(w.get('id')) for _, w in positions]
    if len(ids) != len(set(ids)) or 'null' in ids:
        raise ValueError('missing or repeated native request identity')
    for n, (position, request) in enumerate(positions):
        following = positions[n + 1][0] if n + 1 < len(positions) else len(frames)
        replies = [i for i, f in enumerate(frames) if f['direction'] == 's2c'
                   and f['wire'].get('id') == request['id'] and 'result' in f['wire']]
        if len(replies) != 1 or not position < replies[0] < following:
            raise ValueError('query or close precedes its native prerequisite response')
    seed = plan['historySeed']
    resumed = responses.get(json.dumps(requests[2]['id'])) or {}
    if (requests[2]['params'].get('threadId') != seed['sessionId']
            or requests[2]['params'].get('model') != plan['model']
            or resumed.get('thread', {}).get('id') != seed['sessionId']
            or resumed.get('model') != plan['model']):
        raise ValueError('resume identity or effective model differs')
    agents, cursor = [], None
    for request in requests[3:-1]:
        params = request['params']
        if (params != dict({'ancestorThreadId': seed['sessionId'], 'sourceKinds': ['subAgentThreadSpawn'], 'limit': 10},
                           **({'cursor': cursor} if cursor is not None else {}))):
            raise ValueError('discovery scope or native cursor differs')
        result = responses.get(json.dumps(request['id'])) or {}
        if 'nextCursor' not in result or not isinstance(result.get('data'), list):
            raise ValueError('discovery lacks native coverage evidence')
        for child in result['data']:
            if child.get('parentThreadId') != seed['sessionId']:
                raise ValueError('saved child relationship differs from seed')
            agents.append(child.get('id'))
        cursor = result['nextCursor']
    if cursor is not None or sorted(agents) != sorted(seed['agentIds']) or len(agents) != len(set(agents)):
        raise ValueError('discovered children differ from immutable pre-observation seed')
    closed = responses.get(json.dumps(requests[-1]['id'])) or {}
    if requests[-1]['params'] != {'threadId': seed['sessionId']} or closed.get('status') != 'unsubscribed':
        raise ValueError('resumed observation did not detach its native parent')


def observe(plan, report):
    from openai_codex.client import CodexClient, CodexConfig
    from pydantic import BaseModel, ConfigDict
    class NativeResponse(BaseModel):
        model_config = ConfigDict(extra='allow')
    check_source = not bool(plan.get('_replay'))
    seed = verify_seed(plan, check_source=check_source)
    config = CodexConfig(launch_args_override=tuple(proxy_command(plan)), cwd=(plan.get('_fixtureProject', plan['historyProject']) if plan.get('_fixtureProject') else plan['historyProject']),
                         client_name='official-python-story-recorder', client_title='Protocol recording')
    def reject(method, params):
        raise ValueError('unexpected live request in zero-input existing-child observation')
    with CodexClient(config=config, approval_handler=reject) as client:
        report['receipts'].append({'initialize': serial(client.initialize())})
        def request(method, params):
            response = serial(client.request(method, params, response_model=NativeResponse))
            report['receipts'].append({'existing-query': {'method': method, 'params': dict(params), 'response': response}})
            return response
        response = request('thread/resume', {'threadId': seed['sessionId'], 'model': plan['model'],
                                             'cwd': plan['historyProject']})
        if response.get('thread', {}).get('id') != seed['sessionId'] or response.get('model') != plan['model']:
            raise ValueError('native resume selected a different identity or model')
        children = pages(request, 'thread/list', {'ancestorThreadId': seed['sessionId'],
                                                 'sourceKinds': ['subAgentThreadSpawn'], 'limit': 10})
        if sorted(x['id'] for x in children) != sorted(seed['agentIds']):
            raise ValueError('resumed Session did not discover its pre-existing children')
        request('thread/unsubscribe', {'threadId': seed['sessionId']})
    verify_seed(plan, check_source=check_source)
    frames = [json.loads(line) for line in read_regular(capture_root(plan) / 'raw/raw.jsonl', None).splitlines()]
    require_wire(plan, frames)
