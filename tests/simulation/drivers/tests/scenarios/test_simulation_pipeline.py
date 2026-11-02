"""Current simulation flows through the single-request driver, without real Agents."""
from dataclasses import dataclass
import json
from pathlib import Path
import tempfile
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import patch

from support import BOOTSTRAP
from request_support import request_case
from record_user_story.scenarios import digest, case_recipes
from test_scenarios import Value


class TransportClosedError(RuntimeError):
    pass


class SimulationPipelineTests(unittest.TestCase):
    def test_file_approval_never_accepts_shell_approval_as_file_evidence(self):
        for case, native_kind, expected, decision in (
            ('SDK-CASE-008', 'fileChange', 'passed', 'decline'),
            ('SDK-CASE-009', 'fileChange', 'passed', 'accept'),
            ('SDK-CASE-009', 'commandExecution', 'untriggered', 'decline'),
            ('SDK-CASE-010', 'commandExecution', 'passed', 'accept'),
        ):
            with self.subTest(case=case, native_kind=native_kind):
                decisions, test = [], self
                class Client:
                    def __init__(self, approval_handler, **_): self.answer = approval_handler
                    def __enter__(self): return self
                    def __exit__(self, *_): pass
                    def initialize(self): return {}
                    def thread_start(self, params):
                        test.assertEqual(params['sandbox'], 'read-only')
                        test.assertNotIn('dynamicTools', params)
                        return Value(thread=Value(id='root'), model='selected-model')
                    def turn_start(self, *_args, **_kwargs): return Value(turn=Value(id='turn'))
                    def next_turn_notification(self, _):
                        decisions.append(self.answer('item/' + native_kind + '/requestApproval', {}))
                        return Value(method='turn/completed', payload=Value(threadId='root',
                                     turn=Value(id='turn', status=Value(value='completed'))))
                result = self.worker(case, Client)
                self.assertEqual(result['status'], expected)
                self.assertEqual(decisions, [{'decision': decision}])

    def test_claude_holds_permission_until_an_event_after_control_ack(self):
        import asyncio
        from claude_driver.adapter import claude
        from record_user_story.scenarios import Flow
        @dataclass
        class ResultMessage:
            is_error: bool = False
        modes = []
        class Client:
            def __init__(self, options, **_): self.permission = options.can_use_tool
            async def __aenter__(self): return self
            async def __aexit__(self, *_): pass
            async def query(self, _): pass
            async def set_model(self, _): raise AssertionError('set_model emits no status event')
            async def set_permission_mode(self, mode): modes.append(mode)
            async def receive_response(self):
                yield {'type': 'system', 'subtype': 'init', 'session_id': 'root', 'permissionMode': 'default'}
                task = asyncio.create_task(self.permission('Read', {}, None))
                await asyncio.sleep(0)
                yield {'type': 'system', 'subtype': 'status', 'status': None, 'permissionMode': 'plan', 'session_id': 'root', 'uuid': 'event-id'}
                await task
                yield ResultMessage()
        sdk = ModuleType('claude_agent_sdk')
        sdk.ClaudeSDKClient, sdk.ClaudeAgentOptions = Client, SimpleNamespace
        sdk.Transport = object
        sdk.PermissionResultAllow = sdk.PermissionResultDeny = lambda **kw: kw
        sdk.ResultMessage = ResultMessage
        definition = case_recipes('claude', cases='SDK-CASE-012')[0]
        flow = Flow(definition)
        report = dict(inputs=0, results=0, requests=0, events=0, receipts=[])
        with patch.dict('sys.modules', {'claude_agent_sdk': sdk}):
            asyncio.run(claude(dict(definition, project='/unused', model='fixture'), report, flow))
        flow.finish()
        self.assertIn('held-event', flow.requests)
        self.assertEqual(modes, ['plan', 'default'])

    def worker(self, case, client_class, transport=None, expected_exit=0, raw_frames=None):
        definition = case_recipes('codex', cases=case)[0]
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for name, data in definition['files'].items():
                (root/name).write_text(data)
            plan = dict(version=4, provider='codex', story=definition['storyId'], storyId=definition['storyId'],
                        caseId=case, driverProfile=definition['driverProfile'], root=tmp, project=tmp, raw=str(root/'raw'), model='model',
                        scenario=definition, scenarioSHA256=digest(definition), prompts=definition['prompts'])
            (root / 'plan.json').write_text(json.dumps(plan))
            if transport is not None:
                (root / 'raw').mkdir()
                (root / 'raw/transport.json').write_text(json.dumps(transport))
            if raw_frames is not None:
                (root / 'raw').mkdir(exist_ok=True)
                (root / 'raw/raw.jsonl').write_text(''.join(json.dumps(frame)+'\n' for frame in raw_frames))
            sdk, generated, model = ModuleType('openai_codex.client'), ModuleType('openai_codex.generated.v2_all'), ModuleType('pydantic')
            sdk.CodexClient, sdk.CodexConfig = client_class, SimpleNamespace
            generated.TurnStartParams = SimpleNamespace(model_validate=lambda v: v)
            model.BaseModel, model.ConfigDict = type('BaseModel', (), {}), dict
            errors = ModuleType('openai_codex.errors')
            errors.TransportClosedError = TransportClosedError
            modules = {'openai_codex.errors': errors, 'openai_codex.client': sdk, 'openai_codex.generated.v2_all': generated, 'pydantic': model}
            with patch.dict('sys.modules', modules), patch('record_user_story.drivers.sdk_identity', return_value={'package': 'synthetic', 'version': '1'}):
                request_case(plan, expected_exit)
            return json.loads(next(root.glob('*report.json')).read_text())

    def test_continuation_uses_one_client_and_consumes_both_results_without_idle_wait(self):
        from test_codex_observation_order import evidence
        from enum import Enum
        class Status(str, Enum):
            completed = "completed"
        actions = []
        class Client:
            def __init__(self, **_): self.turn = 0
            def __enter__(self): actions.append('open'); return self
            def __exit__(self, *_): actions.append('closed')
            def initialize(self): return {}
            def thread_start(self, _): return Value(thread=Value(id='root'), model='model')
            def turn_start(self, *_args, **_kwargs):
                self.turn += 1
                actions.append('submit-' + str(self.turn))
                return Value(turn=Value(id=str(self.turn)))
            def next_turn_notification(self, identity):
                actions.append('result-' + identity)
                return Value(method='turn/completed', payload=Value(threadId='root', turn=Value(id=identity, status=Status.completed)))
            def next_notification(self): raise AssertionError('continuation must not wait for idle')
        frames, _ = evidence()
        report = self.worker('SDK-CASE-001', Client, raw_frames=frames)
        self.assertEqual(report['status'], 'passed')
        self.assertEqual(actions, ['open', 'submit-1', 'result-1', 'submit-2', 'result-2', 'closed'])
        self.assertFalse(any('post-result-event' in r for r in report['receipts']))
        self.assertEqual(self.worker('SDK-CASE-001', Client)['status'], 'untriggered')
        class ClosedClient(Client):
            def next_turn_notification(self, identity):
                if identity == '2': raise EOFError('observation ended after first result')
                return super().next_turn_notification(identity)
        self.assertEqual(self.worker('SDK-CASE-001', ClosedClient, raw_frames=frames, expected_exit=1)['status'], 'failed')

    def test_claude_continuation_keeps_native_session_identity(self):
        @dataclass
        class ResultMessage:
            session_id: str = 'root'
            is_error: bool = False
        @dataclass
        class SystemMessage:
            subtype: str = 'session_state_changed'
            data: dict = None
        class Client:
            def __init__(self, **_): pass
            async def __aenter__(self): return self
            async def __aexit__(self, *_): pass
            async def query(self, _): pass
            async def receive_response(self): yield ResultMessage()
            async def receive_messages(self):
                yield SystemMessage(data={'state': 'idle', 'session_id': 'root'})
        sdk = ModuleType('claude_agent_sdk')
        sdk.ClaudeSDKClient, sdk.ClaudeAgentOptions = Client, lambda **kw: kw
        sdk.Transport = object
        sdk.PermissionResultAllow = sdk.PermissionResultDeny = lambda **kw: kw
        sdk.ResultMessage = ResultMessage
        definition = case_recipes('claude', cases='SDK-CASE-001')[0]
        for terminal_session, idle_session, parent, expected in (
            ('root', 'root', None, 'passed'),
            ('root', 'child', None, 'untriggered'),
            ('root', None, None, 'untriggered'),
            ('child', 'root', None, 'untriggered'),
            ('root', 'root', 'child-tool', 'untriggered'),
        ):
            with self.subTest(terminal=terminal_session, idle=idle_session, parent=parent), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                plan = dict(version=4, provider='claude', story=definition['storyId'], storyId=definition['storyId'],
                            caseId=definition['caseId'], driverProfile=definition['driverProfile'], root=tmp, project=tmp,
                            model='', scenario=definition, scenarioSHA256=digest(definition), prompts=definition['prompts'])
                path = root/'plan.json'
                path.write_text(json.dumps(plan))
                frames = [
                    {'direction': 'c2s', 'wire': {'type': 'user', 'session_id': 'default', 'parent_tool_use_id': None}},
                    {'direction': 's2c', 'wire': {'type': 'system', 'subtype': 'session_state_changed', 'session_id': 'root', 'state': 'idle'}},
                    {'direction': 's2c', 'wire': {'type': 'result', 'session_id': terminal_session}},
                    {'direction': 's2c', 'wire': {'type': 'system', 'subtype': 'session_state_changed',
                     'session_id': idle_session, 'parent_tool_use_id': parent, 'state': 'idle'}},
                    {'direction': 'c2s', 'wire': {'type': 'user', 'session_id': 'default', 'parent_tool_use_id': None}},
                ]
                (root/'raw').mkdir()
                (root/'raw/raw.jsonl').write_text(''.join(json.dumps(frame)+'\n' for frame in frames))
                with patch.dict('sys.modules', {'claude_agent_sdk': sdk}), patch('record_user_story.drivers.sdk_identity', return_value={'package': 'synthetic', 'version': '1'}):
                    request_case(plan)
                self.assertEqual(json.loads(next(root.glob('*report.json')).read_text())['status'], expected)

    def test_configuration_acknowledgement_does_not_claim_effect_or_override_turns(self):
        actions = []
        class Client:
            def __init__(self, **_): self.turn = 0
            def __enter__(self): return self
            def __exit__(self, *_): pass
            def initialize(self): return {}
            def thread_start(self, _): return Value(thread=Value(id='root'), model='model')
            def request(self, method, params, **_):
                actions.append(('configure', params))
                return {}
            def turn_start(self, *_args, **kwargs):
                self.turn += 1
                actions.append(('input', kwargs['params']))
                return Value(turn=Value(id=str(self.turn)))
            def next_turn_notification(self, identity):
                return Value(method='turn/completed', payload=Value(turn=Value(id=identity, status=Value(value='completed'))))
        report = self.worker('SDK-CASE-022', Client)
        self.assertEqual(report['status'], 'passed')
        self.assertEqual([item[0] for item in actions], ['configure', 'configure', 'input', 'input'])
        for kind, submitted in actions:
            if kind == 'input':
                self.assertNotIn('model', submitted)
                self.assertNotIn('effort', submitted)
        evidence = report['configurationEvidence']
        self.assertTrue(all(item['acknowledged'] for item in evidence['requests']))
        self.assertEqual(evidence['observed'], [])
        self.assertEqual(evidence['effectiveScope'], 'unknown')
        trace = [step['step'] for step in report['flowTrace']]
        self.assertLess(trace.index('configuration-acknowledged'), trace.index('submit'))

    def test_close_requires_explicit_close_rejected_input_and_reaped_process(self):
        actions = []
        class Client:
            def __init__(self, **_): self.closed = False
            def __enter__(self): return self
            def __exit__(self, *_): pass
            def initialize(self): return {}
            def thread_start(self, _): return Value(thread=Value(id='root'), model='model')
            def turn_start(self, *_args, **_kwargs):
                if self.closed:
                    actions.append('rejected')
                    raise TransportClosedError('Codex process is not running')
                actions.append('submit')
                return Value(turn=Value(id='turn'))
            def next_turn_notification(self, _):
                actions.append('result')
                return Value(method='turn/completed', payload=Value(turn=Value(id='turn', status=Value(value='completed'))))
            def close(self):
                actions.append('close')
                self.closed = True
        report = self.worker('SDK-CASE-031', Client, {'captureStatus': 'complete', 'cleanupComplete': True, 'reaped': True})
        self.assertEqual(report['status'], 'passed')
        self.assertEqual(actions, ['submit', 'result', 'close', 'rejected'])
        self.assertEqual(report['sessionClose'], {'interactionClosed': True, 'postCloseRejected': True, 'processClosed': True})
        class InvalidClient(Client):
            def turn_start(self, *args, **kwargs):
                if self.closed:
                    raise TypeError('wrong argument')
                return super().turn_start(*args, **kwargs)
        failed = self.worker('SDK-CASE-031', InvalidClient,
                             {'captureStatus': 'complete', 'cleanupComplete': True, 'reaped': True}, expected_exit=1)
        self.assertEqual(failed['status'], 'failed')
        self.assertFalse(failed['sessionClose']['postCloseRejected'])


    def test_direct_codex_child_requires_parent_output_and_completion(self):
        for missing, expected, exit_code in (
            (None, 'passed', 0), ('parent', 'untriggered', 0),
            ('output', 'untriggered', 0), ('completion', 'untriggered', 0),
            ('identity', 'failed', 1), ('failure', 'failed', 1),
        ):
            with self.subTest(missing=missing):
                class Client:
                    def __init__(self, approval_handler, **_):
                        self.answer, self.event = approval_handler, 0
                    def __enter__(self): return self
                    def __exit__(self, *_): pass
                    def initialize(self): return {}
                    def thread_start(self, _): return Value(thread=Value(id='root'))
                    def turn_start(self, *_args, **_kwargs): return Value(turn=Value(id='root-turn'))
                    def next_turn_notification(self, _):
                        self.event += 1
                        if self.event == 1:
                            self.answer('item/tool/call', {'threadId': 'child', 'turnId': 'child-turn', 'tool': 'fixture_read',
                                                         'arguments': {'path': 'child-fixture.txt'}})
                            return Value(method='item/completed', payload=Value(threadId='root',
                                         item={'type': 'collabAgentToolCall', 'receiverThreadIds': ['child']}))
                        return Value(method='turn/completed', payload=Value(threadId='root',
                                     turn=Value(id='root-turn', status=Value(value='completed'))))
                    def thread_read(self, _child, **_):
                        child = {'id': 'other' if missing == 'identity' else 'child', 'parentThreadId': 'root',
                                 'turns': [{'id': 'child-turn', 'status': 'completed',
                                            'items': [{'type': 'agentMessage', 'text': 'Read the fixture token.'}]}]}
                        if missing == 'parent': child.pop('parentThreadId')
                        if missing == 'output': child['turns'][0]['items'] = []
                        if missing == 'completion': child['turns'][0]['status'] = 'inProgress'
                        if missing == 'failure': child['turns'][0]['status'] = 'failed'
                        return {'thread': child}
                report = self.worker('SDK-CASE-016', Client, expected_exit=exit_code)
                self.assertEqual(report['status'], expected)
                if expected == 'passed':
                    self.assertEqual(report['childEvidence'], [{'childThreadId': 'child', 'parentThreadId': 'root',
                                     'completedTurnIds': ['child-turn'], 'outputObserved': True}])

    def test_direct_claude_child_requires_correlated_native_lifecycle(self):
        @dataclass
        class ResultMessage:
            session_id: str = 'root'
            is_error: bool = False
        @dataclass
        class AssistantMessage:
            content: list
            parent_tool_use_id: str = 'agent-call'
            session_id: str = 'root'
        @dataclass
        class TaskStartedMessage:
            task_id: str = 'task-identity'
            tool_use_id: str = 'agent-call'
            session_id: str = 'root'
        @dataclass
        class TaskNotificationMessage:
            task_id: str = 'task-identity'
            tool_use_id: str = 'agent-call'
            session_id: str = 'root'
            status: str = 'completed'
        for missing, expected, code in (
            (None, 'passed', 0), ('parent', 'untriggered', 0), ('output', 'untriggered', 0),
            ('completion', 'untriggered', 0), ('task', 'untriggered', 0),
            ('read-context', 'untriggered', 0), ('failure', 'failed', 1),
        ):
            with self.subTest(missing=missing), tempfile.TemporaryDirectory() as tmp:
                class Client:
                    def __init__(self, options, **_): self.permission = options.can_use_tool
                    async def __aenter__(self): return self
                    async def __aexit__(self, *_): pass
                    async def query(self, _):
                        await self.permission('Agent', {}, Value(tool_use_id='agent-call', agent_id=None))
                        await self.permission('Read', {}, Value(tool_use_id='read-call', agent_id='agent-identity'))
                    async def receive_response(self):
                        yield TaskStartedMessage()
                        yield AssistantMessage([{'type': 'tool_use', 'name': 'Read',
                                                'id': 'other-read' if missing == 'read-context' else 'read-call'}],
                                               parent_tool_use_id='unrelated' if missing == 'parent' else 'agent-call')
                        if missing != 'output':
                            yield AssistantMessage([{'type': 'text', 'text': 'Read the fixture token.'}])
                        if missing != 'completion':
                            yield TaskNotificationMessage(task_id='other-task' if missing == 'task' else 'task-identity',
                                                          status='failed' if missing == 'failure' else 'completed')
                        yield ResultMessage()
                sdk = ModuleType('claude_agent_sdk')
                sdk.ClaudeSDKClient, sdk.ClaudeAgentOptions = Client, SimpleNamespace
                sdk.AgentDefinition = SimpleNamespace
                sdk.Transport = object
                sdk.PermissionResultAllow = sdk.PermissionResultDeny = lambda **kw: kw
                sdk.ResultMessage = ResultMessage
                definition = case_recipes('claude', cases='SDK-CASE-016')[0]
                plan = dict(version=4, provider='claude', story=definition['storyId'], storyId=definition['storyId'],
                            caseId=definition['caseId'], driverProfile=definition['driverProfile'], root=tmp, project=tmp,
                            model='', scenario=definition, scenarioSHA256=digest(definition), prompts=definition['prompts'])
                path = Path(tmp)/'plan.json'
                path.write_text(json.dumps(plan))
                with patch.dict('sys.modules', {'claude_agent_sdk': sdk}), patch('record_user_story.drivers.sdk_identity', return_value={'package': 'synthetic', 'version': '1'}):
                    request_case(plan, code)
                report = json.loads((Path(tmp)/'report.json').read_text())
                self.assertEqual(report['status'], expected)
                if expected == 'passed':
                    self.assertEqual(report['childEvidence'], {'sessionId': 'root', 'parentToolUseId': 'agent-call',
                                     'taskId': 'task-identity', 'agentIds': ['agent-identity'],
                                     'outputObserved': True, 'completed': True})

    def test_history_discovers_then_reads_metadata_and_messages_after_close(self):
        for metadata in (True, False):
            with self.subTest(metadata=metadata), tempfile.TemporaryDirectory() as tmp:
                actions = []
                @dataclass
                class ResultMessage:
                    is_error: bool = False
                class Client:
                    def __init__(self, **_): pass
                    async def __aenter__(self): return self
                    async def __aexit__(self, *_): actions.append('close')
                    async def query(self, _, session_id):
                        self_test.assertEqual(session_id, '')
                        actions.append('input')
                    async def receive_response(self): yield ResultMessage()
                self_test = self
                sdk = ModuleType('claude_agent_sdk')
                sdk.ClaudeSDKClient, sdk.ClaudeAgentOptions = Client, lambda **kw: SimpleNamespace(**kw)
                sdk.Transport = object
                sdk.PermissionResultAllow = sdk.PermissionResultDeny = lambda **kw: kw
                sdk.ResultMessage = ResultMessage
                def discover(**_):
                    actions.append('list')
                    return [Value(session_id='discovered')]
                def info(identity, **_):
                    actions.append('info-' + identity)
                    return Value(session_id=identity) if metadata else None
                def messages(identity, **_):
                    actions.append('messages-' + identity)
                    return [Value(type='user', message={'role': 'user', 'content': 'saved'})]
                sdk.list_sessions, sdk.get_session_info, sdk.get_session_messages = discover, info, messages
                definition = case_recipes('claude', cases='SDK-CASE-033')[0]
                plan = dict(version=4, provider='claude', story=definition['storyId'], storyId=definition['storyId'],
                            caseId=definition['caseId'], driverProfile='history', root=tmp, project=tmp, model='',
                            scenario=definition, scenarioSHA256=digest(definition), prompts=definition['prompts'])
                path = Path(tmp) / 'plan.json'
                path.write_text(json.dumps(plan))
                with patch.dict('sys.modules', {'claude_agent_sdk': sdk}), patch('record_user_story.drivers.sdk_identity', return_value={'package': 'synthetic', 'version': '1'}):
                    request_case(plan)
                report = json.loads((Path(tmp) / 'report.json').read_text())
                self.assertEqual(report['status'], 'passed' if metadata else 'untriggered')
                self.assertEqual(actions[:4], ['input', 'close', 'list', 'info-discovered'])
                self.assertEqual('messages-discovered' in actions, metadata)


if __name__ == '__main__':
    unittest.main()
