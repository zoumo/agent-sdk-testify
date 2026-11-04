"""Capture/replay contracts exercised with owned fake processes, never real agents."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import record_user_story
from peer_support import identity, prepare_record, current_capture, mock_command, run_mock
from record_user_story.environment import capture_root, replay_receipt, private_json, recording_temp_root
from record_user_story.scenarios import case_recipes, _validate, recipe, digest


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
                native.write_text('import sys\nsys.stdin.buffer.readline()\nsys.stdout.buffer.write(' + repr(response) + ')\nsys.stdout.buffer.flush()\n')
                definition = recipe(provider, 'conversation', case_id='two-inputs')
                plan = dict(version=4, provider=provider, story='conversation', storyId='conversation', caseId='two-inputs',
                            driverProfile='continuation', scenario=definition, scenarioSHA256=digest(definition), prompts=definition['prompts'],
                            cli=str(Path(sys.executable).resolve()), cliSHA256=hashlib.sha256(Path(sys.executable).read_bytes()).hexdigest(),
                            root=str(root), tempRoot=str(recording_temp_root()),
                            home=str(root/'home'), config=str(root/'config'), project=str(root/'project'),
                            raw=str(root/'raw'), args=[str(native)], model='')
                for name, value in definition['files'].items():
                    (root/'project'/name).write_text(value)
                plan['agentMock'] = identity()
                private_json(root/'plan.json', plan)
                env = {'PATH': '/missing'}
                session = prepare_record(root, plan)
                captured = subprocess.run([identity()['path'], 'agent', 'record', '--session-dir', str(session),
                                           '--cmd', str(Path(sys.executable).resolve()), '--', str(native)], input=request,
                                          capture_output=True, timeout=10, cwd=root, env=env)
                self.assertEqual(captured.returncode, 0, (captured.stderr, {p.name: p.read_text() for p in root.glob('*diagnostics*.json')}))
                self.assertEqual(captured.stdout, response)
                capture = current_capture(session)
                transport = json.loads((capture/'raw/transport.json').read_text())
                self.assertTrue(transport['cleanupComplete'])
                raw = capture/'raw/raw.jsonl'
                self.assertIn(b'900719925474099312345', raw.read_bytes())
                self.assertIn(b'1.2300', raw.read_bytes())
                schema = json.loads((Path(record_user_story.__file__).parent/'schemas/wire.schema.json').read_text())
                for line in raw.read_text().splitlines():
                    _validate(json.loads(line), schema)
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


    def test_public_agent_result_locates_raw_and_mock_receipt_separately(self):
        for command in ('record', 'mock'):
            with self.subTest(command=command), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                execution = root / 'execution.json'
                results = root / 'results.jsonl'
                receipt = root / 'actual-receipt.json'
                material = root / 'immutable-material'
                capture = root / 'execution-capture'
                descriptor = {'executionId': 'actual', 'sessionId': 'session', 'command': command,
                              'artifactRoot': str(capture), 'artifact': {'path': str(material)}}
                execution.write_text(json.dumps(descriptor))
                started = {'status': 'running', 'executionId': 'actual', 'sessionId': 'session',
                           'connectionId': 'main', 'processId': 42, 'command': command,
                           'execution': str(execution), 'receipt': str(receipt)}
                finished = dict(started, status='passed')
                results.write_text(json.dumps(started) + '\n' + json.dumps(finished) + '\n')
                plan = {'root': str(root / 'driver'), '_agentResults': {'main': str(results)}}
                self.assertEqual(capture_root(plan), capture if command == 'record' else material)
                if command == 'mock':
                    expected = {'fullConsumption': True, 'clientEOF': True, 'nativeLaunched': False}
                    receipt.write_text(json.dumps(expected))
                    self.assertEqual(replay_receipt(plan), expected)
                finished['executionId'] = 'other-run'
                results.write_text(json.dumps(started) + '\n' + json.dumps(finished) + '\n')
                with self.assertRaises(ValueError):
                    capture_root(plan)


    def test_default_catalog_has_no_go_story_identity(self):
        with patch.dict(os.environ, {}, clear=True):
            for provider in ('codex', 'claude'):
                definitions = case_recipes(provider)
                self.assertEqual([item['caseId'] for item in definitions], ['two-inputs'])
                self.assertEqual(definitions[0]['storyId'], 'conversation')
                self.assertNotIn('offlineTest', definitions[0])


if __name__ == '__main__':
    unittest.main()
