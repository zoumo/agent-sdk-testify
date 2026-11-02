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
from peer_support import identity
from record_user_story.environment import private_json, recording_temp_root
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
                captured = subprocess.run([identity()['path'], 'protocol', 'record', '--output', str(root/'raw'),
                                           '--', sys.executable, str(native)], input=request,
                                          capture_output=True, timeout=10, cwd=root, env=env)
                self.assertEqual(captured.returncode, 0, (captured.stderr, {p.name: p.read_text() for p in root.glob('*diagnostics*.json')}))
                self.assertEqual(captured.stdout, response)
                transport = json.loads((root/'raw/transport.json').read_text())
                self.assertTrue(transport['cleanupComplete'])
                raw = root/'raw/raw.jsonl'
                self.assertIn(b'900719925474099312345', raw.read_bytes())
                self.assertIn(b'1.2300', raw.read_bytes())
                schema = json.loads((Path(record_user_story.__file__).parent/'schemas/wire.schema.json').read_text())
                for line in raw.read_text().splitlines():
                    _validate(json.loads(line), schema)
                native.unlink()  # Replay must be independent of the recorded executable.
                receipt = root/'receipt.json'
                command = [identity()['path'], 'protocol', 'mock', '--provider', provider,
                           '--scenario', str(raw), '--receipt', str(receipt)]
                replayed = subprocess.run(command, input=rebound, capture_output=True, timeout=10, cwd=root, env=env)
                self.assertEqual(replayed.returncode, 0, replayed.stderr)
                self.assertEqual(replayed.stdout, replay_response)
                result = json.loads(receipt.read_text())
                self.assertTrue(result['fullConsumption'])
                self.assertFalse(result['nativeLaunched'])
                mismatch = subprocess.run(command[:-2], input=b'{}\n', capture_output=True, timeout=10, cwd=root, env=env)
                self.assertEqual(mismatch.returncode, 1)


    def test_default_catalog_has_no_go_story_identity(self):
        with patch.dict(os.environ, {}, clear=True):
            for provider in ('codex', 'claude'):
                definitions = case_recipes(provider)
                self.assertEqual([item['caseId'] for item in definitions], ['two-inputs'])
                self.assertEqual(definitions[0]['storyId'], 'conversation')
                self.assertNotIn('offlineTest', definitions[0])


if __name__ == '__main__':
    unittest.main()
