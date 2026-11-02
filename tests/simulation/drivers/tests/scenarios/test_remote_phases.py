import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from codex_driver.remote import verify_phase_files, REQUIRED_PHASE_FILES


class RemotePhaseTests(unittest.TestCase):
    def test_probe_raw_and_service_identity_are_immutable(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            raw = root / 'remote-probe/raw'
            raw.mkdir(parents=True)
            (raw / 'raw.jsonl').write_text('{"native":9007199254740993}\n')
            for name in ('manifest.json', 'transport.json', 'stderr.raw'):
                (raw / name).write_text('{}')
            (root / 'remote-service.json').write_text(json.dumps({'serverPID': 42, 'probeSucceeded': True}))
            files = {name: hashlib.sha256((root / name).read_bytes()).hexdigest() for name in REQUIRED_PHASE_FILES}
            self.assertEqual(set(files), REQUIRED_PHASE_FILES)
            verify_phase_files(root, files)
            (raw / 'raw.jsonl').write_text('{"native":9007199254740992}\n')
            with self.assertRaises(ValueError):
                verify_phase_files(root, files)

    def test_missing_phase_cannot_verify_as_empty_success(self):
        with tempfile.TemporaryDirectory() as name:
            with self.assertRaises(ValueError):
                verify_phase_files(Path(name), {})

    def test_rejects_escape_and_symlink(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            for relative in ('../outside', '/absolute', 'remote-probe/../../outside', 'remote-probe/other/raw.jsonl'):
                with self.subTest(relative=relative), self.assertRaises(ValueError):
                    verify_phase_files(root, {relative: 'irrelevant'})
            source = root / 'source'
            source.write_text('{}')
            (root / 'remote-service.json').symlink_to(source)
            with self.assertRaises(ValueError):
                verify_phase_files(root, {'remote-service.json': 'irrelevant'})

    def test_native_connections_use_only_the_supplied_main_and_probe_peers(self):
        from types import ModuleType, SimpleNamespace
        from unittest.mock import patch
        from codex_driver.remote import codex
        class Closed(RuntimeError):
            pass
        commands = []
        class Client:
            def __init__(self, config):
                commands.append(config.launch_args_override)
                self.closed = False
            def __enter__(self): return self
            def __exit__(self, *_): self.closed = True
            def initialize(self): return {}
            def thread_start(self, _):
                return SimpleNamespace(thread=SimpleNamespace(id='root'), model='model')
            def turn_start(self, *_):
                if self.closed: raise Closed()
                return SimpleNamespace(turn=SimpleNamespace(id='turn'))
            def next_turn_notification(self, _):
                return SimpleNamespace(method='turn/completed', payload={
                    'threadId': 'root', 'turn': {'id': 'turn', 'status': 'completed'}})
            def thread_read(self, *_args, **_kwargs):
                return {'thread': {'id': 'root', 'turns': [{'id': 'turn'}]}}
        class Flow:
            def inputs(self): return ['input']
            def submit(self): pass
            def result(self): pass
        sdk, errors = ModuleType('openai_codex.client'), ModuleType('openai_codex.errors')
        sdk.CodexClient, sdk.CodexConfig = Client, SimpleNamespace
        errors.TransportClosedError = Closed
        peer = {'command': '/provided-main', 'args': ['main-arg'],
                'probe': {'command': '/provided-probe', 'args': ['probe-arg']}}
        plan = {'_peer': peer, 'project': '/workspace', 'model': 'model'}
        report = dict(receipts=[], inputs=0, results=0, events=0)
        with patch.dict('sys.modules', {'openai_codex.client': sdk, 'openai_codex.errors': errors}):
            codex(plan, report, Flow())
        self.assertEqual(commands, [('/provided-main', 'main-arg'), ('/provided-probe', 'probe-arg')])
        self.assertEqual(report['results'], 1)
        self.assertTrue(report['receipts'][-1]['remote-close']['secondConnectionReadSucceeded'])
