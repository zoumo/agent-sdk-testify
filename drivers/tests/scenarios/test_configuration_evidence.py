import json
import sys
import tempfile
from pathlib import Path
import unittest
from copy import deepcopy

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'common'))
from record_user_story.configuration_evidence import assess
from record_user_story.scenarios import Untriggered


class ConfigurationEvidenceTests(unittest.TestCase):
    def test_budget_ack_model_and_effort_do_not_establish_effect(self):
        plan = {'provider': 'claude', 'scenario': {'parameters': {
            'setting': 'thinking', 'configurationTiming': 'before-first-input'}}}
        request = {'type': 'control_request', 'request_id': 'budget', 'request': {
            'subtype': 'set_max_thinking_tokens', 'max_thinking_tokens': 1024}}
        reply = {'type': 'control_response', 'response': {'request_id': 'budget', 'subtype': 'success'}}
        frames = [{'direction': 'c2s', 'wire': request}, {'direction': 's2c', 'wire': reply},
                  {'direction': 's2c', 'wire': {'type': 'assistant', 'message': {'model': 'selected-model'}}}]
        report = {'configurationEvidence': {'requests': [{'requested': request['request'], 'acknowledged': True}],
                  'observed': [{'model': 'selected-model', 'effort': 'high'}], 'effectiveScope': 'unknown'}}
        with self.assertRaisesRegex(Untriggered, 'budget'):
            assess(plan, frames, report)
        self.assertEqual(report['configurationEvidence']['effectiveScope'], 'unknown')

    def test_display_requires_native_forwarding_and_correlated_success_not_visual_effect(self):
        for provider in ('claude', 'codex'):
            with self.subTest(provider=provider):
                claude = provider == 'claude'
                requested = {'thinking_display': 'omitted'} if claude else {'summary': 'none'}
                request = ({'type': 'control_request', 'request_id': 'display-1',
                            'request': dict(requested, subtype='set_max_thinking_tokens')}
                           if claude else {'id': 'display-1', 'method': 'thread/settings/update', 'params': requested})
                reply = ({'type': 'control_response', 'response': {'request_id': 'display-1', 'subtype': 'success'}}
                         if claude else {'id': 'display-1', 'result': {}})
                plan = {'provider': provider, 'scenario': {'parameters': {'setting': 'display'}}}
                report = {'configurationEvidence': {'requests': [{'requested': requested, 'acknowledged': True}]}}
                frames = [{'direction': 'c2s', 'wire': request}, {'direction': 's2c', 'wire': reply}]
                assess(plan, frames, report)
                self.assertIn('visual effect not asserted', report['configurationEvidence']['effectiveScope'])
                for failure in ('missing-request', 'wrong-value', 'wrong-id', 'error', 'duplicate', 'early-response'):
                    invalid = deepcopy(frames)
                    if failure == 'missing-request':
                        invalid = invalid[1:]
                    elif failure == 'wrong-value':
                        invalid[0]['wire']['request' if claude else 'params'].update(
                            {'thinking_display': 'visible'} if claude else {'summary': 'auto'})
                    elif failure == 'wrong-id':
                        if claude:
                            invalid[1]['wire']['response']['request_id'] = 'other'
                        else:
                            invalid[1]['wire']['id'] = 'other'
                    elif failure == 'error':
                        if claude:
                            invalid[1]['wire']['response']['subtype'] = 'error'
                        else:
                            invalid[1]['wire'].pop('result')
                            invalid[1]['wire']['error'] = {'code': -32602, 'message': 'unsupported'}
                    elif failure == 'duplicate':
                        invalid.append(deepcopy(invalid[1]))
                    else:
                        invalid.reverse()
                    with self.subTest(failure=failure), self.assertRaises(Untriggered):
                        assess(plan, invalid, deepcopy(report))

    def test_same_value_and_ack_only_do_not_prove_switch(self):
        plan = {'provider': 'claude', 'model': 'B', 'scenario': {'parameters': {
            'setting': 'model', 'configurationTiming': 'before-first-input'}}}
        report = {'configurationEvidence': {'requests': [{'requested': {'model': 'B'}, 'acknowledged': True}]}}
        with self.assertRaises(Untriggered):
            assess(plan, [], report)
        plan['initialModel'] = 'A'
        with self.assertRaises(Untriggered):
            assess(plan, [], report)
        observed = {'direction': 's2c', 'wire': {'type': 'assistant', 'message': {'model': 'B'}}}
        with self.assertRaises(Untriggered):
            assess(plan, [observed], report)
        request = {'direction': 'c2s', 'wire': {'type': 'control_request', 'request': {'subtype': 'set_model', 'model': 'B'}}}
        assess(plan, [request, observed], report)
        self.assertEqual(report['configurationEvidence']['observedModels'], ['B'])
        plan['scenario'] = {'configuration': {'timing': 'before-first-input', 'modelSource': 'session'}}
        with self.assertRaises(Untriggered):
            assess(plan, [request], report)
        assess(plan, [request, observed], report)

    def test_media_requires_content_answer(self):
        plan = {'provider': 'claude', 'scenario': {'parameters': {'inputKind': 'image'}}}
        for text, passed in [('Nothing was supplied', False), ('Not blue', False), ('blue', True)]:
            frames = [{'direction': 's2c', 'wire': {'type': 'result', 'result': text}}]
            if passed:
                assess(plan, frames, {'receipts': []})
            else:
                with self.assertRaises(Untriggered):
                    assess(plan, frames, {'receipts': []})

    def test_effort_requires_creation_runtime_and_later_native_observation(self):
        claude_plan = {'provider': 'claude', 'args': ['--effort', 'low'], 'scenario': {'parameters': {
            'setting': 'effort', 'configurationTiming': 'between-inputs',
            'effort': 'low', 'targetEffort': 'high'}}}
        claude_frames = [
            {'direction': 'c2s', 'wire': {'type': 'user', 'parent_tool_use_id': None}},
            {'direction': 's2c', 'wire': {'type': 'result', 'parent_tool_use_id': None}},
            {'direction': 'c2s', 'wire': {'type': 'control_request', 'request_id': 'effort',
                                          'request': {'subtype': 'apply_flag_settings',
                                                      'settings': {'effortLevel': 'high'}}}},
            {'direction': 's2c', 'wire': {'type': 'control_response', 'response': {
                'subtype': 'success', 'request_id': 'effort', 'response': {}}}},
            {'direction': 'c2s', 'wire': {'type': 'control_request', 'request_id': 'readback',
                                          'request': {'subtype': 'get_settings'}}},
            {'direction': 's2c', 'wire': {'type': 'control_response', 'response': {
                'subtype': 'success', 'request_id': 'readback',
                'response': {'effective': {'effortLevel': 'high'}}}}},
            {'direction': 'c2s', 'wire': {'type': 'user', 'parent_tool_use_id': None}},
            {'direction': 's2c', 'wire': {'type': 'result', 'parent_tool_use_id': None}},
        ]
        report = {'receipts': []}
        assess(claude_plan, claude_frames, report)
        self.assertEqual(report['configurationEvidence']['observedEffort'], 'high')
        for failure in ('missing-readback', 'ack-only', 'wrong-readback', 'budget-substitute', 'wrong-creation'):
            invalid = deepcopy(claude_frames)
            if failure == 'missing-readback':
                invalid = [frame for frame in invalid if frame['wire'].get('request_id') != 'readback']
            elif failure == 'ack-only':
                invalid = invalid[:4] + invalid[6:]
            elif failure == 'wrong-readback':
                invalid[5]['wire']['response']['response']['effective']['effortLevel'] = 'low'
            elif failure == 'budget-substitute':
                invalid.insert(2, {'direction': 'c2s', 'wire': {
                    'type': 'control_request', 'request_id': 'budget',
                    'request': {'subtype': 'set_max_thinking_tokens', 'max_thinking_tokens': 1024}}})
            else:
                claude_plan['args'] = ['--effort', 'medium']
            with self.subTest(failure=failure), self.assertRaises(Untriggered):
                assess(claude_plan, invalid, {'receipts': []})
            claude_plan['args'] = ['--effort', 'low']

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

    def test_effort_creation_reads_the_recorded_launch_plan(self):
        frames = [
            {'direction': 'c2s', 'wire': {'type': 'user', 'parent_tool_use_id': None}},
            {'direction': 's2c', 'wire': {'type': 'result', 'parent_tool_use_id': None}},
            {'direction': 'c2s', 'wire': {'type': 'control_request', 'request_id': 'effort',
                                          'request': {'subtype': 'apply_flag_settings',
                                                      'settings': {'effortLevel': 'high'}}}},
            {'direction': 's2c', 'wire': {'type': 'control_response', 'response': {
                'subtype': 'success', 'request_id': 'effort', 'response': {}}}},
            {'direction': 'c2s', 'wire': {'type': 'control_request', 'request_id': 'readback',
                                          'request': {'subtype': 'get_settings'}}},
            {'direction': 's2c', 'wire': {'type': 'control_response', 'response': {
                'subtype': 'success', 'request_id': 'readback',
                'response': {'effective': {'effortLevel': 'high'}}}}},
            {'direction': 'c2s', 'wire': {'type': 'user', 'parent_tool_use_id': None}},
            {'direction': 's2c', 'wire': {'type': 'result', 'parent_tool_use_id': None}},
        ]
        with tempfile.TemporaryDirectory() as tmp:
            plan = {'provider': 'claude', 'root': tmp,
                    'args': ['--output-format', 'stream-json'],
                    'scenario': {'case': {'id': 'SDK-CASE-027'}, 'parameters': {
                        'setting': 'effort', 'configurationTiming': 'between-inputs',
                        'effort': 'low', 'targetEffort': 'high'}}}
            (Path(tmp) / 'plan.json').write_text(json.dumps({
                'provider': 'claude', 'caseId': 'SDK-CASE-027',
                'args': ['--effort', 'low']}))
            report = {'receipts': []}
            assess(plan, frames, report)
            self.assertEqual(report['configurationEvidence']['observedEffort'], 'high')

            (Path(tmp) / 'plan.json').write_text(json.dumps({
                'provider': 'claude', 'caseId': 'SDK-CASE-027',
                'args': ['--effort', 'medium']}))
            with self.assertRaisesRegex(Untriggered, 'creation effort'):
                assess(plan, frames, {'receipts': []})
