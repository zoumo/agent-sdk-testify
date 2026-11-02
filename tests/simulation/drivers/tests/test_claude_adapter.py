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
