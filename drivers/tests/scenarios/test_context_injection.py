"""Synthetic evidence tests; these inputs are never official recordings."""
from copy import deepcopy
import unittest

import support
from record_user_story.context_injection import assess
from record_user_story.scenarios import case_recipes


def synthetic(provider, timing):
    definition = case_recipes(provider, cases='SDK-CASE-037' if timing == 'idle' else 'SDK-CASE-038')[0]
    context_value = definition['parameters']['contextValue']
    plan = {'provider': provider, 'scenario': definition}
    frames = []

    def frame(direction, native):
        frames.append({'direction': direction, 'wire': native})

    if provider == 'codex':
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
    else:
        def query():
            frame('c2s', {'type': 'user', 'shouldQuery': True, 'message': {'role': 'user', 'content': 'query'}})

        def inject():
            frame('c2s', {'type': 'user', 'shouldQuery': False, 'message': {
                'role': 'user', 'content': [{'type': 'text', 'text':
                    'The stored vault code is ' + context_value + '.'}]}})
            frame('s2c', {'type': 'result', 'num_turns': 0, 'session_id': 'root',
                          'is_error': False, 'subtype': 'success', 'result': '',
                          'duration_api_ms': 0, 'usage': {'output_tokens': 0}})

        def result(output):
            frame('s2c', {'type': 'assistant', 'message': {'content': [{'type': 'text', 'text': output}]}})
            frame('s2c', {'type': 'result', 'num_turns': 1, 'session_id': 'root', 'is_error': False})

        query()
        frame('s2c', {'type': 'stream_event'})
        if timing != 'idle':
            inject()
        result('Ready')
        if timing == 'idle':
            inject()
        query()
        result(context_value)
    return plan, frames


class ContextInjectionTests(unittest.TestCase):
    def test_idle_and_running_require_subsequent_use_without_requiring_current_use(self):
        for provider in ('codex', 'claude'):
            for timing in ('idle', 'during-execution'):
                with self.subTest(provider=provider, timing=timing):
                    plan, frames = synthetic(provider, timing)
                    report = {}
                    assess(plan, frames, report)
                    self.assertFalse(report['contextInjection']['currentQueryUsedFact'])
                    self.assertTrue(report['contextInjection']['subsequentQueryUsedFact'])

    def test_wrong_answer_and_leaked_query_are_rejected(self):
        for provider in ('codex', 'claude'):
            plan, frames = synthetic(provider, 'idle')
            for change in ('answer', 'query'):
                with self.subTest(provider=provider, change=change):
                    changed = deepcopy(frames)
                    if change == 'answer':
                        for frame in changed:
                            native = frame['wire']
                            if native.get('method') == 'item/completed':
                                native['params']['item']['text'] = 'wrong'
                            if native.get('type') == 'assistant':
                                native['message']['content'][0]['text'] = 'wrong'
                    else:
                        changed[0]['wire']['leaked'] = plan['scenario']['parameters']['contextValue']
                    with self.assertRaises(ValueError):
                        assess(plan, changed, {})

    def test_acknowledgement_cannot_hide_an_assistant_turn(self):
        for provider in ('codex', 'claude'):
            plan, frames = synthetic(provider, 'idle')
            if provider == 'codex':
                acknowledgement = next(frame['wire'] for frame in frames if 'result' in frame['wire'])
                acknowledgement['result'] = {'unexpected': True}
            else:
                acknowledgement = next(frame['wire'] for frame in frames if frame['wire'].get('num_turns') == 0)
                acknowledgement['usage']['output_tokens'] = 10
            with self.assertRaises(ValueError):
                assess(plan, frames, {})

    def test_idle_injection_must_not_emit_assistant_activity(self):
        for provider in ('codex', 'claude'):
            plan, frames = synthetic(provider, 'idle')
            injected = next(index for index, frame in enumerate(frames)
                            if frame['wire'].get('method') == 'thread/inject_items'
                            or frame['wire'].get('shouldQuery') is False)
            native = ({'method': 'turn/started', 'params': {'threadId': 'root', 'turn': {'id': 'unexpected'}}}
                      if provider == 'codex' else {'type': 'assistant', 'message': {'content': []}})
            frames.insert(injected + 1, {'direction': 's2c', 'wire': native})
            with self.assertRaises(ValueError):
                assess(plan, frames, {})


if __name__ == '__main__':
    unittest.main()
