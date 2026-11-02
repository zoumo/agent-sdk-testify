"""Exercise the local MCP barrier through stdio, without launching an agent."""
import json
from pathlib import Path
import queue
import subprocess
import sys
import tempfile
import threading
import unittest

from support import BOOTSTRAP
from record_user_story import child_gate


class ChildGateTests(unittest.TestCase):
    def test_wait_keeps_dispatch_live_and_releases_only_after_root(self):
        with tempfile.TemporaryDirectory() as tmp:
            marker = Path(tmp) / 'released'
            proc = subprocess.Popen([sys.executable, child_gate.__file__, str(marker)],
                                    stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            replies = queue.Queue()
            reader = threading.Thread(target=lambda: [replies.put(json.loads(line)) for line in proc.stdout])
            reader.start()
            def send(identity, method, params):
                proc.stdin.write(json.dumps(dict(jsonrpc='2.0', id=identity, method=method, params=params)) + '\n')
                proc.stdin.flush()
            try:
                send(1, 'initialize', {'protocolVersion': '2024-11-05'})
                self.assertEqual(replies.get(timeout=2)['result']['capabilities'], {'tools': {}})
                send(2, 'tools/list', {})
                self.assertEqual(replies.get(timeout=2)['result']['tools'][0]['name'], 'wait_for_root')
                send(3, 'tools/call', {'name': 'wait_for_root', 'arguments': {}})
                send(4, 'ping', {})
                self.assertEqual(replies.get(timeout=2)['id'], 4)
                send(5, 'tools/call', {'name': 'wait_for_root', 'arguments': {'path': '../other'}})
                self.assertEqual(replies.get(timeout=2)['error']['code'], -32602)
                self.assertTrue(replies.empty())
                marker.touch()
                result = replies.get(timeout=2)
                self.assertEqual(result['id'], 3)
                self.assertNotIn('isError', result['result'])
            finally:
                proc.stdin.close()
                proc.wait(timeout=2)
                reader.join(timeout=2)
                proc.stdout.close()
                error = proc.stderr.read()
                proc.stderr.close()
                self.assertEqual(proc.returncode, 0, error)

    def test_eof_cancels_unreleased_wait(self):
        with tempfile.TemporaryDirectory() as tmp:
            result = subprocess.run([sys.executable, child_gate.__file__, str(Path(tmp) / 'never')],
                input=json.dumps(dict(jsonrpc='2.0', id=1, method='tools/call',
                                      params={'name': 'wait_for_root', 'arguments': {}})) + '\n',
                capture_output=True, text=True, timeout=2)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, '')

    def test_timeout_is_an_error_not_a_release(self):
        from io import StringIO
        class Input:
            def __iter__(self):
                yield json.dumps(dict(id=1, method='tools/call', params={'name': 'wait_for_root', 'arguments': {}}))
                self.assert_sent.wait(2)
        sent = threading.Event()
        class Output(StringIO):
            def flush(self): sent.set()
        source, output = Input(), Output()
        source.assert_sent = sent
        child_gate.serve(source, output, Path('/nonexistent/child-gate'), timeout=0)
        self.assertTrue(json.loads(output.getvalue())['result']['isError'])
