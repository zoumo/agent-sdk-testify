"""Capture/replay contracts exercised with owned fake processes, never real agents."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from peer_support import identity, prepare_fixture, current_capture, mock_command, run_mock


class RecordingTests(unittest.TestCase):
    def test_capture_can_be_consumed_by_mock_without_sdk_or_repository(self):
        for provider in ('codex', 'claude'):
            with self.subTest(provider=provider), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()/'conversation'/'two-inputs'
                root.mkdir(parents=True)
                for name in ('home', 'config', 'project', 'tmp'):
                    (root / name).mkdir()
                if provider == 'codex':
                    request = b'{"id":41,"method":"ping","params":{"future":null}}\n'
                    response = b'{"id":41,"result":{"future":900719925474099312345,"ratio":1.2300}}\n'
                    rebound = request.replace(b'41', b'99')
                    replay_response = response.replace(b'41', b'99', 1)
                else:
                    request = b'{"type":"control_request","request_id":"q41","request":{"subtype":"initialize"}}\n'
                    response = b'{"type":"control_response","response":{"request_id":"q41","subtype":"success","response":{"future":900719925474099312345,"ratio":1.2300}}}\n'
                    rebound = request.replace(b'q41', b'q99')
                    replay_response = response.replace(b'q41', b'q99')
                native = root / 'fake_native.py'
                native.write_text('#!' + sys.executable + '\nimport sys\nsys.stdin.buffer.readline()\nsys.stdout.buffer.write(' + repr(response) + ')\nsys.stdout.buffer.flush()\n')
                native.chmod(0o700)
                plan = {'provider': provider, 'caseId': 'two-inputs', 'project': str(root/'project'),
                        'scenario': {'files': {'numbers.txt': '7\n11\n'}}}
                env = {'PATH': '/missing'}
                fixture = prepare_fixture(root, plan)
                startup = ['app-server'] if provider == 'codex' else ['--input-format', 'stream-json', '--output-format', 'stream-json', '--permission-prompt-tool', 'stdio']
                captured = subprocess.run([identity()['path'], 'agent', 'record', '--fixture-dir', str(fixture),
                                           '--cmd', str(native), '--', *startup], input=request,
                                          capture_output=True, timeout=10, cwd=root, env=env)
                self.assertEqual(captured.returncode, 0, (captured.stderr, {p.name: p.read_text() for p in root.glob('*diagnostics*.json')}))
                self.assertEqual(captured.stdout, response)
                capture = current_capture(fixture)
                transport = json.loads((capture/'raw/transport.json').read_text())
                self.assertTrue(transport['cleanupComplete'])
                raw = capture/'raw/raw.jsonl'
                self.assertIn(b'900719925474099312345', raw.read_bytes())
                self.assertIn(b'1.2300', raw.read_bytes())
                for line in raw.read_text().splitlines():
                    frame = json.loads(line)
                    self.assertEqual(set(frame), {'ordinal', 'direction', 'channel', 'epoch', 'elapsedMs', 'wire'})
                    self.assertIsInstance(frame['wire'], dict)
                    self.assertIn(frame['direction'], ('c2s', 's2c'))
                    self.assertIn(frame['channel'], ('protocol', 'control'))
                    self.assertEqual(frame['epoch'], 1)
                    self.assertGreaterEqual(frame['ordinal'], 1)
                    self.assertGreaterEqual(frame['elapsedMs'], 0)
                native.unlink()  # Replay must be independent of the recorded executable.
                receipt = root/'receipt.json'
                command = mock_command(provider, raw, '--receipt', str(receipt))
                replayed = run_mock(command, input=rebound, capture_output=True, timeout=10, cwd=root, env=env)
                self.assertEqual(replayed.returncode, 0, replayed.stderr)
                self.assertEqual(replayed.stdout, replay_response)
                result = json.loads(receipt.read_text())
                self.assertTrue(result['fullConsumption'])
                self.assertFalse(result['nativeLaunched'])
                mismatch = subprocess.run(mock_command(provider, raw), input=b'{}\n', capture_output=True, timeout=10, cwd=root, env=env)
                self.assertEqual(mismatch.returncode, 1)
