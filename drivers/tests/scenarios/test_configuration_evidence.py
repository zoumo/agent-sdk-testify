import sys
from pathlib import Path
import unittest
from copy import deepcopy

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'common'))
from record_user_story.configuration_evidence import assess
from record_user_story.scenarios import Untriggered


class ConfigurationEvidenceTests(unittest.TestCase):
    def test_display_requires_native_forwarding_and_correlated_success_not_visual_effect(self):
        requested = {'summary': 'none'}
        request = {'id': 'display-1', 'method': 'thread/settings/update', 'params': requested}
        reply = {'id': 'display-1', 'result': {}}
        plan = {'provider': 'codex', 'scenario': {'parameters': {'setting': 'display'}}}
        report = {'configurationEvidence': {'requests': [{'requested': requested, 'acknowledged': True}]}}
        frames = [{'direction': 'c2s', 'wire': request}, {'direction': 's2c', 'wire': reply}]
        assess(plan, frames, report)
        self.assertIn('visual effect not asserted', report['configurationEvidence']['effectiveScope'])
        for failure in ('missing-request', 'wrong-value', 'wrong-id', 'error', 'duplicate', 'early-response'):
            invalid = deepcopy(frames)
            if failure == 'missing-request':
                invalid = invalid[1:]
            elif failure == 'wrong-value':
                invalid[0]['wire']['params']['summary'] = 'auto'
            elif failure == 'wrong-id':
                invalid[1]['wire']['id'] = 'other'
            elif failure == 'error':
                invalid[1]['wire'] = {'id': 'display-1', 'error': {'code': -32602, 'message': 'unsupported'}}
            elif failure == 'duplicate':
                invalid.append(deepcopy(invalid[1]))
            else:
                invalid.reverse()
            with self.subTest(failure=failure), self.assertRaises(Untriggered):
                assess(plan, invalid, deepcopy(report))

    def test_media_requires_content_answer(self):
        plan = {'provider': 'codex', 'scenario': {'parameters': {'inputKind': 'image'}}}
        for text, passed in [('Nothing was supplied', False), ('Not blue', False), ('blue', True)]:
            frames = [{'direction': 's2c', 'wire': {
                'method': 'item/completed', 'params': {'item': {'type': 'agentMessage', 'text': text}},
            }}]
            if passed:
                assess(plan, frames, {'receipts': []})
            else:
                with self.assertRaises(Untriggered):
                    assess(plan, frames, {'receipts': []})

    def test_effort_uses_creation_and_target_payload_between_inputs(self):
        codex_plan = {'provider': 'codex', 'scenario': {'parameters': {
            'setting': 'effort', 'configurationTiming': 'between-inputs',
            'effort': 'low', 'targetEffort': 'medium'}}}
        codex_frames = [
            {'direction': 'c2s', 'wire': {'id': 'start', 'method': 'thread/start',
                                          'params': {'config': {'model_reasoning_effort': 'low'}}}},
            {'direction': 's2c', 'wire': {'id': 'start', 'result': {'thread': {
                'id': 'thread-1', 'reasoningEffort': 'low'}}}},
            {'direction': 'c2s', 'wire': {'id': 'turn-1', 'method': 'turn/start',
                                          'params': {'threadId': 'thread-1'}}},
            {'direction': 's2c', 'wire': {'id': 'turn-1', 'result': {'turn': {'id': 'execution-1'}}}},
            {'direction': 's2c', 'wire': {'method': 'turn/completed',
                                          'params': {'turn': {'id': 'execution-1'}}}},
            {'direction': 'c2s', 'wire': {'id': 'effort', 'method': 'thread/settings/update',
                                          'params': {'threadId': 'thread-1', 'effort': 'medium'}}},
            {'direction': 's2c', 'wire': {'id': 'effort', 'result': {}}},
            {'direction': 's2c', 'wire': {'method': 'thread/settings/updated', 'params': {
                'threadId': 'thread-1', 'threadSettings': {'effort': 'medium'}}}},
            {'direction': 'c2s', 'wire': {'id': 'turn-2', 'method': 'turn/start',
                                          'params': {'threadId': 'thread-1'}}},
            {'direction': 's2c', 'wire': {'id': 'turn-2', 'result': {'turn': {'id': 'execution-2'}}}},
            {'direction': 's2c', 'wire': {'method': 'turn/completed',
                                          'params': {'turn': {'id': 'execution-2'}}}},
        ]
        report = {'receipts': []}
        assess(codex_plan, codex_frames, report)
        self.assertEqual(report['configurationEvidence']['observedEffort'], 'medium')
        for failure in ('missing-observation', 'wrong-creation', 'wrong-runtime', 'ack-only'):
            invalid = deepcopy(codex_frames)
            if failure == 'missing-observation':
                invalid.pop(7)
            elif failure == 'wrong-creation':
                invalid[0]['wire']['params']['config']['model_reasoning_effort'] = 'medium'
            elif failure == 'wrong-runtime':
                invalid[5]['wire']['params']['effort'] = 'high'
            else:
                invalid.pop(7)
            with self.subTest(failure=failure), self.assertRaises(Untriggered):
                assess(codex_plan, invalid, {'receipts': []})
