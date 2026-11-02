"""Exercise permissive capture with local native-process fixtures only."""
import asyncio
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace, ModuleType
import unittest
from unittest.mock import patch

from support import BOOTSTRAP
from peer_support import identity
from record_user_story.drivers import run
from record_user_story.environment import sdk_identity
from record_user_story.scenarios import recipe, digest
from record_user_story.stream import json_messages


class CapturePolicyTests(unittest.TestCase):


    def test_async_transport_accepts_large_json_lines(self):
        async def receive():
            reader = asyncio.StreamReader()
            reader.feed_data(json.dumps({'text': 'x' * 2200000}).encode() + b'\n')
            reader.feed_eof()
            return [message async for message in json_messages(reader)]
        self.assertEqual(len(asyncio.run(receive())[0]['text']), 2200000)

    def test_sdk_versions_are_observed_without_an_allowlist(self):
        with patch('importlib.metadata.version', return_value='99.1.2'):
            self.assertEqual(sdk_identity('codex'), {'package': 'openai-codex', 'version': '99.1.2'})

    def test_background_child_waits_for_its_native_terminal_result(self):
        from record_user_story import children
        waits = []
        class Value(SimpleNamespace):
            def model_dump(self, **_):
                return {key: value.model_dump() if isinstance(value, Value) else value
                        for key, value in vars(self).items()}
        class Client:
            def __init__(self, **_):
                self.events = iter([
                    Value(method='item/completed', payload=Value(item={'type': 'subAgentActivity', 'agentThreadId': 'child'})),
                    Value(method='turn/completed', payload=Value(turn=Value(id='root-turn', status=Value(value='completed')))),
                ])
            def __enter__(self): return self
            def __exit__(self, *_): pass
            def initialize(self): return {}
            def thread_start(self, _): return Value(thread=Value(id='root'))
            def turn_start(self, *_): return Value(turn=Value(id='root-turn'))
            def next_turn_notification(self, _): return next(self.events)
            def thread_read(self, *_ , **__):
                return {'thread': {'id': 'child', 'parentThreadId': 'root',
                                   'turns': [{'id': 'child-turn', 'status': 'completed' if waits else 'inProgress'}]}}
            def wait_for_turn_completed(self, identity):
                waits.append(identity)
                return {'threadId': 'child', 'turn': {'id': identity, 'status': 'completed'}}
        sdk = ModuleType('openai_codex.client')
        sdk.CodexClient, sdk.CodexConfig = Client, lambda **kw: kw
        report = dict(inputs=0, results=0, events=0, requests=0, receipts=[], facts=[], gaps=[])
        plan = dict(_peer={'command': '/unused-peer', 'args': []}, driverProfile='background-children', story='SDK-US-005', root='/unused', project='/unused', model='', prompts=['synthetic'])
        with patch.dict('sys.modules', {'openai_codex.client': sdk}):
            children.codex(plan, report)
        self.assertEqual(waits, ['child-turn'])
        self.assertTrue(any('post-root-native-child/completed' in item for item in report['receipts']))
        self.assertNotIn('native post-root child completion is not established', report['gaps'])

    def test_api_error_is_not_downgraded_to_a_case_gap(self):
        with tempfile.TemporaryDirectory() as tmp:
            definition = recipe('codex', 'SDK-US-001', case_id='SDK-CASE-001')
            plan = dict(version=4, provider='codex', story='SDK-US-001', storyId='SDK-US-001', caseId='SDK-CASE-001', driverProfile='continuation', root=tmp,
                        scenario=definition, scenarioSHA256=digest(definition),
                        prompts=definition['prompts'], cliVersion='99.0.0')
            with patch('record_user_story.drivers.sdk_identity', return_value={'package': 'fixture', 'version': '99'}), \
                 patch('codex_driver.adapter.codex', side_effect=RuntimeError('API 503')):
                self.assertEqual(run(plan), 1)
            report = json.loads((Path(tmp)/'report.json').read_text())
            self.assertEqual(report['captureStatus'], 'failed')
            self.assertEqual(report['officialSDK']['version'], '99')

    def test_changed_sdk_version_is_recorded_without_blocking_capture(self):
        with tempfile.TemporaryDirectory() as tmp:
            definition = recipe('codex', 'SDK-US-001', case_id='SDK-CASE-001')
            plan = dict(version=4, provider='codex', story='SDK-US-001', storyId='SDK-US-001', caseId='SDK-CASE-001', driverProfile='continuation', root=tmp,
                        scenario=definition, scenarioSHA256=digest(definition),
                        prompts=definition['prompts'], cliVersion='99.0.0',
                        officialSDK={'package': 'fixture', 'version': '1'})
            def complete(_plan, report, flow):
                for _ in flow.inputs():
                    flow.submit()
                    flow.result()
                    if flow.completed == 1:
                        flow.post_result_event()
            with patch('record_user_story.drivers.sdk_identity', return_value={'package': 'fixture', 'version': '2'}), \
                 patch('codex_driver.adapter.codex', side_effect=complete):
                self.assertEqual(run(plan), 0)
            report = json.loads((Path(tmp)/'report.json').read_text())
            self.assertEqual(report['captureStatus'], 'complete')
            self.assertEqual(report['officialSDK']['version'], '2')
