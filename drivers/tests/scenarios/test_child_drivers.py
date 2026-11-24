"""Child driver regressions with synthetic SDK dispatch, never real agents."""
import json
from pathlib import Path
import tempfile
import tomllib
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import patch

from support import BOOTSTRAP
from record_user_story import children
from record_user_story.child_policy import CHILD_FILE, CHILD_TEXT, child_task
from record_user_story.scenarios import case_recipes
from test_runtime_config import runtime
from test_scenarios import Value


def report():
    return dict(inputs=0, results=0, events=0, requests=0, receipts=[], facts=[], gaps=[])


def plan(provider, root, case='SDK-CASE-019'):
    definition = case_recipes(provider, cases=case)[0]
    return dict(scenario=definition, driverProfile=definition['driverProfile'], root=root, project=root,
                model='selected-model', prompts=definition['prompts'], _peer={'command': '/unused'})


class CodexChildTests(unittest.TestCase):
    def test_replay_checks_wire_order_instead_of_consumer_scheduling(self):
        root = {'direction': 's2c', 'wire': {'method': 'turn/completed', 'params':
                {'threadId': 'root', 'turn': {'id': 'root-turn', 'status': 'completed'}}}}
        read = {'direction': 's2c', 'wire': {'method': 'item/tool/call', 'params':
                {'threadId': 'child', 'turnId': 'child-turn', 'tool': 'fixture_read'}}}
        for frames, valid in (([root, read], True), ([read, root], False), ([read], False), ([root], False)):
            with self.subTest(frames=frames), tempfile.TemporaryDirectory() as tmp:
                (Path(tmp) / 'raw.jsonl').write_text(''.join(json.dumps(f) + '\n' for f in frames))
                result = report()
                children.assess_codex_background_order({'raw': tmp}, result, 'root', 'root-turn', {'child': {'child-turn'}})
                self.assertEqual(not result['gaps'], valid)

    def test_background_read_and_completion_stay_after_root_even_if_snapshot_already_completed(self):
        for mode in ('inProgress', 'completed', 'early-read', 'root-read', 'wrong-path', 'duplicate',
                     'wrong-completion', 'failed-root', 'replay-ahead'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as tmp:
                (Path(tmp) / CHILD_FILE).write_text(CHILD_TEXT)
                current = plan('codex', tmp)
                result, observed, test = report(), [], self
                if mode == 'replay-ahead':
                    current['_replay'] = True
                    current['raw'] = str(Path(tmp) / 'raw')
                    Path(current['raw']).mkdir()
                    frames = [
                        {'direction': 's2c', 'wire': {'method': 'turn/completed', 'params':
                            {'threadId': 'root', 'turn': {'id': 'root-turn', 'status': 'completed'}}}},
                        {'direction': 's2c', 'wire': {'method': 'item/tool/call', 'params':
                            {'threadId': 'child', 'turnId': 'child-turn', 'tool': 'fixture_read'}}},
                    ]
                    (Path(current['raw']) / 'raw.jsonl').write_text(''.join(json.dumps(f) + '\n' for f in frames))
                marker = Path(tmp) / 'tmp/child-root-completed'
                class Client:
                    def __init__(self, approval_handler, **_): self.answer, self.event, self.completed = approval_handler, 0, False
                    def register_turn_notifications(self, identity):
                        test.assertEqual(identity, 'child-turn')
                        observed.append('subscribed')
                    def __enter__(self): return self
                    def __exit__(self, *_): pass
                    def initialize(self):
                        if mode == 'replay-ahead':
                            return {}
                        config = tomllib.loads((Path(tmp) / 'config/config.toml').read_text())
                        gate = config['mcp_servers']['child_gate']
                        test.assertEqual(gate['enabled_tools'], ['wait_for_root'])
                        test.assertEqual(gate.get('tools'), {'wait_for_root': {'approval_mode': 'approve'}})
                        test.assertNotIn('default_tools_approval_mode', gate)
                        test.assertTrue(Path(gate['args'][0]).is_file())
                        test.assertEqual(gate['args'][1], str(marker))
                        role = tomllib.loads((Path(tmp) / 'config/agents/fixture_reader.toml').read_text())
                        test.assertNotIn(CHILD_TEXT.strip(), role['developer_instructions'])
                        test.assertIn('mcp__child_gate__wait_for_root', role['developer_instructions'])
                        return {}
                    def thread_start(self, params):
                        test.assertEqual(params['dynamicTools'][0]['name'], 'fixture_read')
                        return Value(thread=Value(id='root'))
                    def turn_start(self, *_): return Value(turn=Value(id='root-turn'))
                    def read(self, caller='child', path=CHILD_FILE):
                        value = self.answer('item/tool/call', dict(threadId=caller, turnId='child-turn',
                                            tool='fixture_read', arguments={'path': path}))
                        observed.append(value['success'])
                        return value
                    def next_turn_notification(self, _):
                        self.event += 1
                        if self.event == 1:
                            if mode == 'early-read':
                                test.assertFalse(self.read()['success'])
                            if mode == 'replay-ahead':
                                test.assertTrue(self.read()['success'])
                            return Value(method='item/completed', payload=Value(threadId='root',
                                         item={'type': 'subAgentActivity', 'agentThreadId': 'child'}))
                        test.assertFalse(marker.exists())
                        return Value(method='turn/completed', payload=Value(threadId='root',
                                     turn=Value(id='root-turn', status=Value(value='failed' if mode == 'failed-root' else 'completed'))))
                    def thread_read(self, *_args, **_kwargs):
                        test.assertEqual(marker.is_file(), mode != 'replay-ahead')
                        if not any('fixture_read' in receipt for receipt in result['receipts']):
                            test.assertTrue(any('root/completed' in receipt for receipt in result['receipts']))
                            self.read(caller='root' if mode == 'root-read' else 'child',
                                      path='../other' if mode == 'wrong-path' else CHILD_FILE)
                            if mode == 'duplicate':
                                test.assertFalse(self.read()['success'])
                        return {'thread': {'id': 'child', 'parentThreadId': 'root', 'turns': [
                            {'id': 'child-turn', 'status': 'inProgress' if mode == 'inProgress' and not self.completed else 'completed',
                             'items': [{'type': 'agentMessage', 'text': CHILD_TEXT}]}]}}
                    def wait_for_turn_completed(self, identity):
                        self.completed = True
                        test.assertEqual(marker.is_file(), mode != 'replay-ahead')
                        test.assertIn('subscribed', observed)
                        return {'threadId': 'other' if mode == 'wrong-completion' else 'child',
                                'turn': {'id': identity, 'status': 'completed'}}
                sdk = ModuleType('openai_codex.client')
                sdk.CodexClient, sdk.CodexConfig = Client, SimpleNamespace
                with patch.dict('sys.modules', {'openai_codex.client': sdk}):
                    if mode in ('wrong-completion', 'failed-root'):
                        with self.assertRaises((ValueError, RuntimeError)):
                            children.codex(current, result)
                    else:
                        children.codex(current, result)
                        if mode in ('inProgress', 'completed', 'replay-ahead'):
                            self.assertEqual(result['gaps'], [])
                            self.assertEqual(observed, ['subscribed', True])
                            self.assertTrue(result['childEvidence'])
                        else:
                            self.assertTrue(result['gaps'])
                self.assertFalse(marker.exists())
