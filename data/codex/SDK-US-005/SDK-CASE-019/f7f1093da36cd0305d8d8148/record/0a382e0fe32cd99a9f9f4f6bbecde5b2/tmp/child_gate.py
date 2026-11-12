"""A local MCP barrier, separate from the Codex SDK's synchronous reader."""
import json
from pathlib import Path
import sys
import threading
import time


def serve(source, sink, marker, timeout=60):
    write_lock = threading.Lock()
    stopped = threading.Event()
    workers = []

    def send(identity, **payload):
        with write_lock:
            sink.write(json.dumps(dict(jsonrpc='2.0', id=identity, **payload)) + '\n')
            sink.flush()

    def wait(identity):
        deadline = time.monotonic() + timeout
        while not stopped.is_set():
            if marker.is_file():
                send(identity, result={'content': [{'type': 'text', 'text': 'Root completed; read the fixture now.'}]})
                return
            if time.monotonic() >= deadline:
                send(identity, result={'isError': True, 'content': [{'type': 'text', 'text': 'Root completion barrier timed out.'}]})
                return
            stopped.wait(0.01)

    try:
        for line in source:
            request = json.loads(line)
            if 'id' not in request:
                continue
            identity, method = request['id'], request.get('method')
            if method == 'initialize':
                send(identity, result={'protocolVersion': request['params']['protocolVersion'],
                                       'capabilities': {'tools': {}},
                                       'serverInfo': {'name': 'child-gate', 'version': '1'}})
            elif method == 'ping':
                send(identity, result={})
            elif method == 'tools/list':
                send(identity, result={'tools': [{'name': 'wait_for_root',
                     'description': 'Wait until the driver observes successful root completion. Call before fixture_read.',
                     'inputSchema': {'type': 'object', 'properties': {}, 'additionalProperties': False}}]})
            elif method == 'tools/call' and request.get('params') == {'name': 'wait_for_root', 'arguments': {}}:
                worker = threading.Thread(target=wait, args=(identity,))
                workers.append(worker)
                worker.start()
            else:
                send(identity, error={'code': -32602, 'message': 'Unsupported barrier request or arguments'})
    finally:
        stopped.set()
        for worker in workers:
            worker.join()


if __name__ == '__main__':
    serve(sys.stdin, sys.stdout, Path(sys.argv[1]))
