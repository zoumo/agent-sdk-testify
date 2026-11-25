"""Synthetic evidence tests; these inputs are never official recordings."""
from copy import deepcopy
import unittest

import support
from record_user_story.context_injection import assess
from record_user_story.scenarios import case_recipes


def synthetic(timing):
    definition = case_recipes('codex', cases='SDK-CASE-037' if timing == 'idle' else 'SDK-CASE-038')[0]
    context_value = definition['parameters']['contextValue']
    plan = {'provider': 'codex', 'scenario': definition}
    frames = []

    def frame(direction, native):
        frames.append({'direction': direction, 'wire': native})

    def query():
        frame('c2s', {'method': 'turn/start', 'params': {'threadId': 'root', 'input': 'query'}})

    def event(method, turn_id, **fields):
        frame('s2c', {'method': method, 'params': {'threadId': 'root', 'turnId': turn_id, **fields}})

    def inject():
        frame('c2s', {'id': 'inject', 'method': 'thread/inject_items', 'params': {
            'threadId': 'root', 'items': [{'type': 'message', 'role': 'user', 'content': [
                {'type': 'input_text', 'text': 'The stored vault code is ' + context_value + '.'}]}]}})
        frame('s2c', {'id': 'inject', 'result': {}})

    def result(turn_id, output):
        event('item/completed', turn_id, item={'type': 'agentMessage', 'text': output})
        event('turn/completed', turn_id, turn={'id': turn_id, 'status': 'completed'})

    query()
    event('turn/started', 'first', turn={'id': 'first'})
    event('item/started', 'first', item={'type': 'reasoning'})
    if timing != 'idle':
        inject()
    result('first', 'Ready')
    if timing == 'idle':
        inject()
    query()
    event('turn/started', 'second', turn={'id': 'second'})
    result('second', context_value)
    return plan, frames


class ContextInjectionTests(unittest.TestCase):
    def test_idle_and_running_require_subsequent_use_without_requiring_current_use(self):
        for timing in ('idle', 'during-execution'):
            with self.subTest(timing=timing):
                plan, frames = synthetic(timing)
                report = {}
                assess(plan, frames, report)
                self.assertFalse(report['contextInjection']['currentQueryUsedFact'])
                self.assertTrue(report['contextInjection']['subsequentQueryUsedFact'])

    def test_wrong_answer_and_leaked_query_are_rejected(self):
        plan, frames = synthetic('idle')
        for change in ('answer', 'query'):
            with self.subTest(change=change):
                changed = deepcopy(frames)
                if change == 'answer':
                    for frame in changed:
                        native = frame['wire']
                        if native.get('method') == 'item/completed':
                            native['params']['item']['text'] = 'wrong'
                else:
                    changed[0]['wire']['leaked'] = plan['scenario']['parameters']['contextValue']
                with self.assertRaises(ValueError):
                    assess(plan, changed, {})

    def test_acknowledgement_cannot_hide_an_assistant_turn(self):
        plan, frames = synthetic('idle')
        acknowledgement = next(frame['wire'] for frame in frames if 'result' in frame['wire'])
        acknowledgement['result'] = {'unexpected': True}
        with self.assertRaises(ValueError):
            assess(plan, frames, {})

    def test_idle_injection_must_not_emit_assistant_activity(self):
        plan, frames = synthetic('idle')
        injected = next(index for index, frame in enumerate(frames)
                        if frame['wire'].get('method') == 'thread/inject_items')
        native = {'method': 'turn/started', 'params': {'threadId': 'root', 'turn': {'id': 'unexpected'}}}
        frames.insert(injected + 1, {'direction': 's2c', 'wire': native})
        with self.assertRaises(ValueError):
            assess(plan, frames, {})


if __name__ == '__main__':
    unittest.main()
