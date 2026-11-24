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
        assess(frames, copy.deepcopy(base), {'childConfiguration': 'model'}, 'requested')
        for field, value in [('model', None), ('model', 'other'), ('parentThreadId', 'other')]:
            report = copy.deepcopy(base)
            report['receipts'][1]['history-query']['response']['thread'][field] = value
            with self.subTest(field=field, value=value), self.assertRaises(Untriggered):
                assess(frames, report, {'childConfiguration': 'model'}, 'requested')
