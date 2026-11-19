"""Deterministic stdio tool for the Codex approval recording cases."""
import json
import sys

for line in sys.stdin:
    request = json.loads(line)
    if 'id' not in request:
        continue
    response = {'jsonrpc': '2.0', 'id': request['id']}
    method = request.get('method')
    if method == 'initialize':
        response['result'] = {'protocolVersion': request['params']['protocolVersion'],
                              'capabilities': {'tools': {}},
                              'serverInfo': {'name': 'approval_fixture', 'version': '1'}}
    elif method == 'tools/list':
        response['result'] = {'tools': [{'name': 'token', 'description': 'Return a synthetic token.',
                                        'inputSchema': {'type': 'object', 'properties': {},
                                                        'additionalProperties': False}}]}
    elif (method == 'tools/call' and request['params'].get('name') == 'token'
          and request['params'].get('arguments') in ({}, None)):
        response['result'] = {'content': [{'type': 'text', 'text': 'SYNTHETIC_MCP_APPROVED'}]}
    elif method == 'ping':
        response['result'] = {}
    else:
        response['error'] = {'code': -32601, 'message': 'Unexpected MCP request'}
    print(json.dumps(response), flush=True)
