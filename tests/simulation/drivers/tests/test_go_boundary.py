"""Single-request official adapter exercises actual Go capture and mock processes."""
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import patch

from request_support import request_case
from peer_support import identity
from record_user_story.scenarios import recipe, digest
import shlex


class Value(SimpleNamespace):
    def model_dump(self, **_):
        def convert(value):
            if isinstance(value, Value):
                return {k: convert(v) for k, v in vars(value).items()}
            if isinstance(value, list):
                return [convert(v) for v in value]
            return value
        return convert(self)


def value(data):
    return Value(**{k: value(v) if isinstance(v, dict) else v for k, v in data.items()})


class GoBoundaryTests(unittest.TestCase):
    def prepare(self, base, binary=None):
        source = base/'source'
        source.mkdir()
        native = base/'synthetic-native'
        native.write_text('#!' + sys.executable + '''
import json, sys
if '--version' in sys.argv:
    print('synthetic 1.0.0')
    raise SystemExit
for line in sys.stdin:
    request = json.loads(line)
    method = request['method']
    if method == 'initialize': result = {'synthetic': True}
    elif method == 'thread/start': result = {'thread': {'id': 'thread'}, 'model': 'synthetic'}
    else: result = {'turn': {'id': 'turn'}}
    print(json.dumps({'id': request['id'], 'result': result}), flush=True)
    if method == 'turn/start':
        print('{"method":"turn/completed","params":{"turn":{"id":"turn","status":{"value":"completed"}},"opaque":900719925474099312345,"ratio":1.2300}}', flush=True)
''')
        native.chmod(0o700)
        root = base/'dataset/conversation/two-inputs'
        root.mkdir(parents=True)
        definition = recipe('codex', 'conversation', case_id='two-inputs')
        plan = dict(version=4, provider='codex', story='conversation', storyId='conversation', caseId='two-inputs',
                    driverProfile='continuation', root=str(root), project=str(root), raw=str(root/'raw'),
                    model='synthetic', scenario=definition, scenarioSHA256=digest(definition), prompts=definition['prompts'],
                    cli=str(native), officialSDK={'package': 'synthetic-sdk', 'version': '1'})
        return root, plan

    def test_official_adapter_substitute_records_and_replays_through_go(self):
        # Only the SDK implementation is replaced. The real story adapter,
        # frozen process launcher, Go record and Go mock all execute.
        class Client:
            def __init__(self, config, **_):
                self.command = config.launch_args_override
                self.cwd = config.cwd
                self.number = 0
            def __enter__(self):
                self.process = subprocess.Popen(self.command, cwd=self.cwd, stdin=subprocess.PIPE,
                                                stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                return self
            def request(self, method, params=None, **_):
                self.number += 1
                self.process.stdin.write((json.dumps({'id': self.number, 'method': method, 'params': params})+'\n').encode())
                self.process.stdin.flush()
                reply = json.loads(self.process.stdout.readline())
                return value(reply['result'])
            def initialize(self): return self.request('initialize')
            def thread_start(self, params): return self.request('thread/start', params)
            def turn_start(self, thread, inputs, **_): return self.request('turn/start', {'threadId':thread, 'input':inputs})
            def next_turn_notification(self, _):
                item = json.loads(self.process.stdout.readline())
                return Value(method=item['method'], payload=value(item['params']))
            def __exit__(self, *_):
                self.process.stdin.close()
                self.process.stdin = None
                _, diagnostic = self.process.communicate(timeout=10)
                if self.process.returncode:
                    raise RuntimeError(diagnostic.decode())
        sdk, generated, pydantic = ModuleType('openai_codex.client'), ModuleType('openai_codex.generated.v2_all'), ModuleType('pydantic')
        sdk.CodexClient, sdk.CodexConfig = Client, SimpleNamespace
        generated.TurnStartParams = SimpleNamespace(model_validate=lambda v:v)
        pydantic.BaseModel, pydantic.ConfigDict = type('BaseModel', (), {}), dict
        with tempfile.TemporaryDirectory() as temporary:
            root, plan = self.prepare(Path(temporary).resolve())
            modules = {'openai_codex.client': sdk, 'openai_codex.generated.v2_all': generated, 'pydantic': pydantic}
            with patch.dict(sys.modules, modules), patch('record_user_story.drivers.sdk_identity', return_value=plan['officialSDK']):
                binary = identity()['path']
                launcher = root/'record-peer'
                launcher.write_text('#!/bin/sh\nexec ' + shlex.join([binary, 'protocol', 'record', '--output', str(root/'raw'),
                                                                         '--cwd', str(root), '--', plan['cli']]) + '\n')
                launcher.chmod(0o700)
                request_case(plan, peer={'command': str(launcher), 'args': []})
                report = json.loads((root/'report.json').read_bytes())
                self.assertEqual((report['inputs'], report['results'], report['status']), (2, 2, 'passed'))
                before = {p.name: p.read_bytes() for p in (root/'raw').iterdir()}
                go_manifest = json.loads(before['manifest.json'])
                for name, checksum in go_manifest['files'].items():
                    self.assertEqual(hashlib.sha256(before[name]).hexdigest(), checksum)
                Path(plan['cli']).unlink()  # Replay must never look up or run native.
                # The runner supplies a separate output and explicit peer for replay.
                replay_root = root/'replay'
                replay_root.mkdir()
                (replay_root/'raw').mkdir()
                shutil.copyfile(root/'raw/raw.jsonl', replay_root/'raw/raw.jsonl')
                replay_plan = dict(plan, root=str(replay_root), raw=str(replay_root/'raw'))
                launcher = replay_root/'mock-peer'
                receipt = replay_root/'peer.json'
                launcher.write_text('#!/bin/sh\nexec ' + shlex.join([binary, 'protocol', 'mock', '--provider', 'codex',
                                                 '--scenario', str(root/'raw/raw.jsonl'), '--receipt', str(receipt)]) + '\n')
                launcher.chmod(0o700)
                request_case(replay_plan, peer={'command': str(launcher), 'args': []})
                replay_report = next(replay_root.glob('*report.json'))
                self.assertEqual(json.loads(replay_report.read_bytes())['status'], 'passed')
                completion = json.loads(receipt.read_bytes())
                self.assertTrue(completion['fullConsumption'])
                self.assertTrue(completion['clientEOF'])
                self.assertFalse(completion['nativeLaunched'])
                self.assertEqual(completion['recordingSHA256'], hashlib.sha256(before['raw.jsonl']).hexdigest())
                self.assertEqual(before, {p.name: p.read_bytes() for p in (root/'raw').iterdir()})
                self.assertIn(b'1.2300', before['raw.jsonl'])
                self.assertIn(b'900719925474099312345', before['raw.jsonl'])


if __name__ == '__main__':
    unittest.main()
