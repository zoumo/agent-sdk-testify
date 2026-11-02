import copy
import unittest
import tempfile

from record_user_story.child_configuration import assess
from record_user_story.scenarios import Untriggered
from record_user_story.drivers import observe_wire


class ChildConfigurationTests(unittest.TestCase):
    def test_missing_raw_cannot_prove_child_configuration_or_delivery(self):
        with tempfile.TemporaryDirectory() as root:
            for parameter in ('childConfiguration', 'childDetailPolicy'):
                plan = {'root': root, 'provider': 'codex', 'scenario': {'parameters': {parameter: 'model'}}}
                with self.subTest(parameter=parameter), self.assertRaises(Untriggered):
                    observe_wire(plan, {'receipts': []})

    def test_codex_requires_actual_child_model_and_parent_edge(self):
        base = {'receipts': [
            {'thread/start': {'model': 'requested', 'thread': {'id': 'root'}}},
            {'history-query': {'method': 'thread/read', 'response': {'thread': {
                'id': 'child', 'parentThreadId': 'root', 'model': 'requested'}}}}]}
        frames = [{'direction': 's2c', 'wire': {'method': 'item/agentMessage/delta',
                   'params': {'threadId': 'child', 'delta': 'output'}}}]
        assess('codex', frames, copy.deepcopy(base), {'childConfiguration': 'model'}, 'requested')
        for field, value in [('model', None), ('model', 'other'), ('parentThreadId', 'other')]:
            report = copy.deepcopy(base)
            report['receipts'][1]['history-query']['response']['thread'][field] = value
            with self.subTest(field=field, value=value), self.assertRaises(Untriggered):
                assess('codex', frames, report, {'childConfiguration': 'model'}, 'requested')

    def test_claude_preserves_requested_alias_and_requires_native_association(self):
        wires = [
            {'type': 'assistant', 'session_id': 'root', 'message': {'model': 'actual', 'content': [
                {'type': 'tool_use', 'id': 'call', 'name': 'Agent', 'input': {}}]}},
            {'type': 'system', 'subtype': 'task_started', 'session_id': 'root', 'tool_use_id': 'call', 'task_id': 'child'},
            {'type': 'assistant', 'session_id': 'root', 'parent_tool_use_id': 'call', 'message': {
                'model': 'actual', 'content': [{'type': 'text', 'text': 'output'}]}},
            {'type': 'result', 'session_id': 'root'}]
        base = {'receipts': [{'list_subagents': {'sessionId': 'root', 'agentIds': ['child']}}]}
        frames = lambda values: [{'direction': 's2c', 'wire': w} for w in values]
        report = copy.deepcopy(base)
        assess('claude', frames(wires), report, {'childConfiguration': 'model'}, 'provider-alias')
        evidence = report['receipts'][-1]['child-model-inheritance']
        self.assertEqual(evidence['requested'], 'provider-alias')
        self.assertEqual(evidence['rootObservedModel'], 'actual')
        for variant in ['different-model', 'wrong-parent', 'explicit-child-model', 'no-output']:
            bad = copy.deepcopy(wires)
            if variant == 'different-model': bad[2]['message']['model'] = 'other'
            if variant == 'wrong-parent': bad[1]['tool_use_id'] = 'other'
            if variant == 'explicit-child-model': bad[0]['message']['content'][0]['input']['model'] = 'other'
            if variant == 'no-output': bad[2]['message']['content'] = []
            with self.subTest(variant=variant), self.assertRaises(Untriggered):
                assess('claude', frames(bad), copy.deepcopy(base), {'childConfiguration': 'model'}, 'provider-alias')
