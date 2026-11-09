"""Exercise structured input through the adapter's asynchronous SDK seam."""
import asyncio
import sys
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'common'))
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'claude' / 'official'))
from claude_driver.adapter import claude


class ClaudeAdapterTests(unittest.TestCase):
    def test_sdk_consumes_structured_input_for_text_and_media(self):
        for kind, expected in [(None, ['text']), ('image', ['text', 'image']),
                               ('document', ['text', 'document']),
                               ('mixed', ['text', 'image', 'text'])]:
            with self.subTest(kind=kind):
                submitted = []

                class Client:
                    def __init__(self, **kwargs):
                        pass

                    async def __aenter__(self):
                        return self

                    async def __aexit__(self, *args):
                        pass

                    async def query(self, messages):
                        async for message in messages:
                            submitted.append(message)

                    async def receive_response(self):
                        for event in ():
                            yield event

                sdk = SimpleNamespace(ClaudeSDKClient=Client, ClaudeAgentOptions=SimpleNamespace,
                                      Transport=object, PermissionResultAllow=SimpleNamespace,
                                      PermissionResultDeny=SimpleNamespace, ResultMessage=type('ResultMessage', (), {}))
                flow = SimpleNamespace(definition={'parameters': {'inputKind': kind},
                                       'configuration': {}, 'submission': 'sequential-results'},
                                       inputs=lambda: ['Synthetic input'], submit=lambda: None)
                report = {'inputs': 0, 'results': 0, 'receipts': []}
                plan = {'driverProfile': 'structured-input', 'project': '/synthetic'}
                with patch.dict(sys.modules, {'claude_agent_sdk': sdk}):
                    asyncio.run(claude(plan, report, flow))
                self.assertEqual(report['inputs'], 1)
                self.assertEqual(len(submitted), 1)
                self.assertEqual(submitted[0]['message']['role'], 'user')
                self.assertEqual([block['type'] for block in submitted[0]['message']['content']], expected)
                self.assertEqual(submitted[0]['message']['content'][0]['text'], 'Synthetic input')

    def test_multi_select_answer_is_joined_before_native_writeback(self):
        captured = {}

        class PermissionResultAllow:
            def __init__(self, **kwargs):
                captured.update(kwargs)

        class Client:
            def __init__(self, **kwargs):
                self.options = kwargs['options']

            async def __aenter__(self):
                captured['permission'] = await self.options.can_use_tool(
                    'AskUserQuestion',
                    {'questions': [{
                        'question': 'Which synthetic colors?',
                        'multiSelect': True,
                        'options': [{'label': 'Blue'}, {'label': 'Green'}],
                    }]},
                    None,
                )
                return self

            async def __aexit__(self, *args):
                pass

            async def query(self, messages):
                if isinstance(messages, str):
                    return
                async for _ in messages:
                    pass

            async def receive_response(self):
                for event in ():
                    yield event

        sdk = SimpleNamespace(ClaudeSDKClient=Client, ClaudeAgentOptions=SimpleNamespace,
                              Transport=object, PermissionResultAllow=PermissionResultAllow,
                              PermissionResultDeny=SimpleNamespace, ResultMessage=type('ResultMessage', (), {}))
        flow = SimpleNamespace(
            definition={
                'parameters': {'questionCount': 1, 'freeText': False},
                'configuration': {},
                'submission': 'sequential-results',
                'answers': {'choice': ['Blue', 'Green'], 'options': ['Blue', 'Green']},
                'requiredRequests': ['question'],
            },
            inputs=lambda: ['Synthetic input'],
            submit=lambda: None,
            request=lambda name: None,
            issues=[],
        )
        report = {'inputs': 0, 'results': 0, 'events': 0, 'requests': 0, 'receipts': []}
        plan = {'driverProfile': 'questions', 'project': '/synthetic'}
        with patch.dict(sys.modules, {'claude_agent_sdk': sdk}):
            asyncio.run(claude(plan, report, flow))
        self.assertEqual(
            captured['updated_input']['answers'],
            {'Which synthetic colors?': 'Blue, Green'},
        )

    def test_effort_change_uses_apply_flag_settings_and_reads_settings(self):
        class Query:
            def __init__(self):
                self.requests = []

            async def _send_control_request(self, request):
                self.requests.append(request)
                if request['subtype'] == 'get_settings':
                    return {'effective': {'effortLevel': 'high'}}
                return {}

        queries = []

        class ResultMessage:
            is_error = False

        class Client:
            def __init__(self, **_):
                self._query = Query()
                queries.append(self._query)

            async def __aenter__(self):
                return self

            async def __aexit__(self, *args):
                pass

            async def query(self, _):
                pass

            async def receive_response(self):
                yield ResultMessage()

        sdk = SimpleNamespace(ClaudeSDKClient=Client, ClaudeAgentOptions=SimpleNamespace,
                              Transport=object, PermissionResultAllow=SimpleNamespace,
                              PermissionResultDeny=SimpleNamespace, ResultMessage=ResultMessage)
        flow = SimpleNamespace(
            definition={
                'parameters': {
                    'configurationTiming': 'between-inputs',
                    'setting': 'effort',
                    'effort': 'low',
                    'targetEffort': 'high',
                },
                'configuration': {},
                'submission': 'sequential-results',
                'answers': {},
                'requiredRequests': [],
            },
            inputs=lambda: ['First synthetic input', 'Second synthetic input'],
            submit=lambda: None,
            result=lambda: None,
            trace=[],
            completed=0,
            request=lambda name: None,
            issues=[],
        )
        report = {'inputs': 0, 'results': 0, 'events': 0, 'requests': 0, 'receipts': []}
        plan = {'driverProfile': 'structured-input', 'project': '/synthetic', 'model': 'fixture'}
        with patch.dict(sys.modules, {'claude_agent_sdk': sdk}):
            asyncio.run(claude(plan, report, flow))
        self.assertEqual(queries[0].requests, [
            {'subtype': 'apply_flag_settings', 'settings': {'effortLevel': 'high'}},
            {'subtype': 'get_settings'},
        ])
        variant = next(item['variant-configuration'] for item in report['receipts']
                       if 'variant-configuration' in item)
        self.assertEqual(variant['observed'], {'effective': {'effortLevel': 'high'}})
