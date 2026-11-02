"""Offline branch/order tests; native SDK substitutes never launch a process."""
import asyncio
from copy import deepcopy
from dataclasses import dataclass
import json
from pathlib import Path
import tempfile
from types import ModuleType, SimpleNamespace
import unittest
from unittest.mock import patch

from support import BOOTSTRAP
from record_user_story.scenarios import Flow, Untriggered, recipe as native_recipe, digest, validate_recipe, scenario_directory, select_story_groups, case_recipes
from record_user_story.drivers import run
from codex_driver.adapter import codex
from claude_driver.adapter import claude
from record_user_story.environment import private_json, verify_recording


def recipe(provider, flow_name, directory=None):
    identities = {
        'continuation': ('SDK-US-001', 'SDK-CASE-001'),
        'media': ('SDK-US-001', 'SDK-CASE-002'),
        'active-input': ('SDK-US-001', 'SDK-CASE-005'),
        'tools': ('SDK-US-002', 'SDK-CASE-006'),
        'approval': ('SDK-US-003', 'SDK-CASE-008'),
        'questions': ('SDK-US-004', 'SDK-CASE-013'),
        'child': ('SDK-US-005', 'SDK-CASE-016'),
        'configuration': ('SDK-US-006', 'SDK-CASE-022'),
        'interrupt': ('SDK-US-007', 'SDK-CASE-030'),
        'close': ('SDK-US-007', 'SDK-CASE-031'),
        'history': ('SDK-US-008', 'SDK-CASE-033'),
    }
    story, case = identities.get(flow_name, (flow_name, None))
    return native_recipe(provider, story, directory, case_id=case)


def plan_for(definition, **values):
    values.setdefault('_peer', {'command': '/unused-peer', 'args': []})
    return dict(version=4, story=definition['storyId'], storyId=definition['storyId'],
                caseId=definition['caseId'], driverProfile=definition['driverProfile'],
                provider=definition['provider'], scenario=definition,
                scenarioSHA256=digest(definition), prompts=definition['prompts'], **values)


class Value(SimpleNamespace):
    def model_dump(self, **_):
        def convert(item):
            if isinstance(item, Value):
                return {k: convert(v) for k, v in vars(item).items()}
            return item
        return convert(self)


def report():
    return {'inputs': 0, 'events': 0, 'results': 0, 'requests': 0,
            'receipts': [], 'facts': [], 'gaps': []}


class ScenarioTests(unittest.TestCase):
    def test_story_selection_does_not_accept_case_or_substory_ids(self):
        self.assertEqual(select_story_groups('SDK-US-004,SDK-US-001'), 'SDK-US-004,SDK-US-001')
        for value in ('SDK-US-004.01', 'SDK-CASE-001', 'SDK-US-999', ''):
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, 'stable story ID'):
                select_story_groups(value)

    def test_frozen_dataset_story_selection_preserves_original_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            case = Path(tmp) / 'SDK-US-001' / 'SDK-CASE-001'
            case.mkdir(parents=True)
            (case / 'plan.json').write_text(json.dumps({'storyId': 'SDK-US-001'}))
            self.assertEqual(select_story_groups('SDK-US-001', dataset=tmp), 'SDK-US-001')
            with self.assertRaisesRegex(ValueError, 'stable story ID'):
                select_story_groups('SDK-US-001.01', dataset=tmp)

    def test_catalog_covers_all_goals_and_validates_frozen_definition(self):
        for provider in ('codex', 'claude'):
            for definition in case_recipes(provider):
                story = definition['storyId']
                with self.subTest(provider=provider, case=definition['caseId']):
                    self.assertRegex(definition['caseId'], r'^SDK-CASE-\d{3}$')
                    self.assertEqual(definition['case']['storyId'], story)
                    self.assertEqual(definition['case']['id'], definition['caseId'])
                    self.assertEqual(definition['case']['scope'], definition['scope'])
                    self.assertTrue(definition['case']['outOfScope'])
                    plan = plan_for(definition)
                    self.assertEqual(validate_recipe(plan), definition)
                    changed = deepcopy(plan)
                    changed['scenario']['limits']['inputs'] += 1
                    changed['scenarioSHA256'] = digest(changed['scenario'])
                    with self.assertRaises(ValueError):
                        validate_recipe(changed)
                    self.assertNotIn('offlineTest', definition)
                    if definition['mode'] == 'live-selected-path' and definition['driverProfile'] == 'existing-children':
                        self.assertEqual(definition['prompts'], [])
                        self.assertEqual(definition['limits']['inputs'], 0)
                    else:
                        self.assertEqual(bool(definition['prompts']), definition['mode'] == 'live-selected-path')
        self.assertNotIn('nativeStories', recipe('codex', 'continuation'))
        with self.assertRaises(ValueError):
            recipe('codex', 'SDK-US-020')


    def test_scoped_case_pass_does_not_require_other_us_branches(self):
        for number in ('continuation', 'media', 'active-input', 'tools', 'questions', 'configuration', 'interrupt'):
            with self.subTest(number=number), tempfile.TemporaryDirectory() as tmp:
                definition = recipe('codex', number)
                plan = plan_for(definition, root=tmp, officialSDK={'package': 'fixture', 'version': '0'})
                def adapter(_plan, saved, flow):
                    for _ in flow.inputs():
                        flow.submit()
                        action = flow.progress()
                        if action:
                            flow.action(action, 'accepted')
                        for name in definition['requiredRequests']:
                            flow.request(name)
                        flow.result()
                        if flow.completed == 1 and definition.get('parameters', {}).get('requirePostResultEvent'):
                            flow.post_result_event()
                    if number == 'continuation':
                        from test_codex_observation_order import evidence
                        frames, observed = evidence()
                        raw = Path(tmp) / 'raw'
                        raw.mkdir()
                        (raw / 'raw.jsonl').write_text(''.join(json.dumps(f) + '\n' for f in frames))
                        saved['receipts'] = observed['receipts']
                with patch('record_user_story.drivers.sdk_identity', return_value=plan['officialSDK']), patch('codex_driver.adapter.codex', side_effect=adapter):
                    self.assertEqual(run(plan), 0)
                saved = json.loads((Path(tmp) / 'report.json').read_text())
                self.assertEqual(saved['status'], 'passed')
                self.assertEqual(saved['branchStatus'], 'passed')
                self.assertEqual(saved['case'], definition['case'])
                self.assertEqual(saved['gaps'], [])

    def test_scoped_missing_evidence_cannot_be_excluded(self):
        with tempfile.TemporaryDirectory() as tmp:
            definition = recipe('codex', 'tools')
            plan = plan_for(definition, root=tmp, officialSDK={'package': 'fixture', 'version': '0'})
            def adapter(_plan, saved, flow):
                flow.submit()
                flow.result()
            with patch('record_user_story.drivers.sdk_identity', return_value=plan['officialSDK']), patch('codex_driver.adapter.codex', side_effect=adapter):
                self.assertEqual(run(plan), 0)
            saved = json.loads((Path(tmp) / 'report.json').read_text())
            self.assertEqual(saved['status'], 'untriggered')
            self.assertEqual(saved['captureStatus'], 'complete')
            self.assertTrue(saved['gaps'])

    def test_scoped_child_missing_native_evidence_is_untriggered(self):
        with tempfile.TemporaryDirectory() as tmp:
            definition = recipe('codex', 'child')
            plan = plan_for(definition, childCase=True, root=tmp,
                            officialSDK={'package': 'fixture', 'version': '0'}, case=definition['case'])
            def adapter(_plan, saved):
                saved.update(inputs=1, results=1)
                saved['gaps'].append('no native child identity')
            with patch('record_user_story.drivers.sdk_identity', return_value=plan['officialSDK']), patch('record_user_story.children.codex', side_effect=adapter):
                self.assertEqual(run(plan), 0)
            saved = json.loads((Path(tmp) / 'report.json').read_text())
            self.assertEqual(saved['status'], 'untriggered')
            self.assertEqual(saved['captureStatus'], 'complete')
            self.assertIn('no native child identity', saved['gaps'])

    def test_approval_case_is_one_denied_write(self):
        definition = recipe('claude', 'approval')
        self.assertEqual(definition['limits']['inputs'], 1)
        self.assertEqual(definition['requiredRequests'], ['approval_answer'])
        self.assertEqual(len(definition['prompts']), 1)
        self.assertIn('denied.txt', definition['prompts'][0])

    def test_claude_denied_write_case_never_needs_an_allowed_write(self):
        @dataclass
        class ResultMessage:
            is_error: bool = False
        class Client:
            def __init__(self, options, **_): self.permission = options['can_use_tool']
            async def __aenter__(self): return self
            async def __aexit__(self, *_): pass
            async def query(self, _): pass
            async def receive_response(self):
                decision = await self.permission('Write', {'file_path': 'denied.txt', 'content': 'denied\n'}, None)
                self_test.assertEqual(decision['message'], 'Synthetic denial; do not retry')
                yield ResultMessage()
        self_test = self
        sdk = ModuleType('claude_agent_sdk')
        sdk.ClaudeSDKClient = Client
        sdk.ClaudeAgentOptions = lambda **kw: kw
        sdk.Transport = object
        sdk.PermissionResultAllow = sdk.PermissionResultDeny = lambda **kw: kw
        sdk.ResultMessage = ResultMessage
        definition = recipe('claude', 'approval')
        flow, saved = Flow(definition), report()
        with patch.dict('sys.modules', {'claude_agent_sdk': sdk}):
            asyncio.run(claude(dict(definition, project='/unused', model='fixture'), saved, flow))
        flow.finish()
        self.assertEqual(saved['requests'], 1)
        self.assertEqual(saved['inputs'], 1)
        self.assertEqual(saved['results'], 1)

    def test_configuration_changes_inputs_and_workflow_without_python_changes(self):
        import shutil
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp) / 'scenarios'
            shutil.copytree(scenario_directory(), directory)
            path = directory / 'SDK-US-001.json'
            container = json.loads(path.read_text())
            core = container['cases'][4]
            original = recipe('codex', 'active-input', directory)
            core['inputs']['input-1'] = 'Reply First.'
            core['inputs']['input-2'] = 'Reply Second.'
            core['steps'] = [{'op': 'session.open'},
                             {'op': 'input.submit', 'input': 'input-1'},
                             {'op': 'event.wait', 'event': 'execution.result', 'onMissing': 'untriggered'},
                             {'op': 'input.submit', 'input': 'input-2'},
                             {'op': 'event.wait', 'event': 'execution.result', 'onMissing': 'untriggered'},
                             {'op': 'session.close'}]
            path.write_text(json.dumps(container))
            definition = recipe('codex', 'active-input', directory)
            self.assertNotEqual(digest(original), digest(definition))
            self.assertEqual(definition['prompts'], ['Reply First.', 'Reply Second.'])
            flow = Flow(definition)
            self.assertIsNone(definition['progressAction'])
            for _ in flow.inputs():
                flow.submit()
                self.assertIsNone(flow.progress())
                flow.result()
                if flow.completed == 1 and definition.get('parameters', {}).get('requirePostResultEvent'):
                    flow.post_result_event()
            flow.finish()

    def test_new_scenario_requires_only_configuration_for_supported_operations(self):
        import shutil
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp) / 'scenarios'
            shutil.copytree(scenario_directory(), directory)
            index_path = directory / 'index.json'
            index = json.loads(index_path.read_text())
            index['stories'].append('SDK-US-020')
            index_path.write_text(json.dumps(index))
            core = json.loads((directory / 'SDK-US-001.json').read_text())
            core['id'] = 'SDK-US-020'
            core['cases'] = [core['cases'][0]]
            core['cases'][0]['id'] = 'SDK-CASE-200'
            core['cases'][0]['storyId'] = 'SDK-US-020'
            (directory / 'SDK-US-020.json').write_text(json.dumps(core))
            binding_path = directory / 'bindings/codex.json'
            binding = json.loads(binding_path.read_text())
            binding['cases']['SDK-CASE-200'] = deepcopy(binding['cases']['SDK-CASE-001'])
            binding_path.write_text(json.dumps(binding))
            definition = recipe('codex', 'SDK-US-020', directory)
            self.assertEqual(definition['story'], 'SDK-US-020')
            self.assertEqual(len(definition['prompts']), 2)
            flow = Flow(definition)
            for _ in flow.inputs():
                flow.submit()
                flow.result()
                if flow.completed == 1 and definition.get('parameters', {}).get('requirePostResultEvent'):
                    flow.post_result_event()
            flow.finish()

    def test_invalid_declarative_configuration_is_rejected(self):
        import shutil
        def unknown(core): core['python'] = 'eval()'
        def operation(core): core['steps'][1]['op'] = 'python.eval'
        def reference(core): core['steps'][1]['input'] = 'missing'
        def order(core): core['steps'].reverse()
        def escape(core): core['setup']['files']['../escape'] = 'invalid'
        def budget(core): core['limits']['activitySeconds'] = -1
        def missing(core): core.pop('expect')
        def missing_case(core): core.pop('id')
        def wrong_case(core): core['storyId'] = 'SDK-US-004'
        def empty_scope(core): core['expect']['scope'] = ''
        for change in (unknown, operation, reference, order, escape, budget, missing, missing_case, wrong_case, empty_scope):
            with self.subTest(change=change.__name__), tempfile.TemporaryDirectory() as tmp:
                directory = Path(tmp) / 'scenarios'
                shutil.copytree(scenario_directory(), directory)
                path = directory / 'SDK-US-001.json'
                container = json.loads(path.read_text())
                change(container['cases'][4])
                path.write_text(json.dumps(container))
                with self.assertRaises(ValueError):
                    recipe('codex', 'active-input', directory)

    def test_duplicate_json_keys_are_rejected(self):
        import shutil
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp) / 'scenarios'
            shutil.copytree(scenario_directory(), directory)
            path = directory / 'SDK-US-001.json'
            path.write_text('{"version":1,"version":2}')
            with self.assertRaisesRegex(ValueError, 'duplicate scenario JSON key'):
                recipe('codex', 'active-input', directory)

    def test_no_supplement_fallback_when_progress_is_absent(self):
        flow = Flow(recipe('codex', 'active-input'))
        self.assertEqual(len(flow.inputs()), 1)
        flow.submit()
        flow.result()
        self.assertIsNone(flow.progress())
        with self.assertRaises(Untriggered):
            flow.finish()
        self.assertEqual(flow.sent, 1)

    def test_native_rejection_is_retained_without_second_result(self):
        flow = Flow(recipe('codex', 'active-input'))
        flow.submit()
        action = flow.progress()
        flow.action(action, 'rejected')
        self.assertIsNone(flow.progress())
        with self.assertRaises(ValueError):
            flow.action(action, 'accepted')
        flow.result()
        flow.finish()
        self.assertIn({'step': 'submit-supplement', 'outcome': 'rejected'}, flow.trace)

    def test_request_variations_are_recorded_without_blocking_capture(self):
        flow = Flow(recipe('codex', 'tools'))
        flow.request('fixture_read')
        flow.submit()
        with self.assertRaises(ValueError):
            flow.submit()  # The driver must still execute its configured steps.
        flow.request('fixture_write')
        flow.request('fixture_read')
        flow.request('fixture_read')
        flow.result()
        with self.assertRaises(Untriggered):
            flow.finish()
        self.assertEqual(flow.requests, ['fixture_read', 'fixture_write', 'fixture_read', 'fixture_read'])
        with self.assertRaises(ValueError):
            flow.submit()

    def test_non_live_paths_do_not_import_or_launch_sdk(self):
        with tempfile.TemporaryDirectory() as tmp:
            for definition in case_recipes('codex'):
                if definition['mode'] == 'live-selected-path':
                    continue
                root = Path(tmp) / definition['caseId']
                root.mkdir()
                plan = plan_for(definition, root=str(root))
                with patch('record_user_story.drivers.sdk_identity', side_effect=AssertionError('SDK accessed')):
                    self.assertEqual(run(plan), 0)
                saved = json.loads((root / 'report.json').read_text())
                self.assertEqual(saved['inputs'], 0)
                self.assertEqual(saved['status'], definition['mode'])


    def test_manifest_refuses_changed_plan(self):
        import hashlib
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            private_json(root / 'plan.json', {'fixture': 'original'})
            private_json(root / 'manifest.json', {'files': {}, 'planSHA256': hashlib.sha256((root / 'plan.json').read_bytes()).hexdigest()})
            verify_recording(root)
            (root / 'plan.json').write_text('{}')
            with self.assertRaises(ValueError):
                verify_recording(root)

    def test_codex_native_adapter_uses_barrier_once_and_waits_between_turns(self):
        for number, progress in [('continuation', True), ('active-input', True), ('active-input', False), ('interrupt', True), ('interrupt', False)]:
            with self.subTest(number=number, progress=progress):
                actions = []
                launch_cwds, native_cwds = [], []
                class Client:
                    def __init__(self, **kwargs):
                        launch_cwds.append(kwargs['config']['cwd'])
                        self.turns = 0
                        self.events = {}
                    def __enter__(self): return self
                    def __exit__(self, *_): actions.append('closed')
                    def initialize(self): return {}
                    def thread_start(self, params):
                        native_cwds.append(params['cwd'])
                        return Value(thread=Value(id='root'), model='fixture')
                    def turn_start(self, *_, **__):
                        self.turns += 1
                        identity = str(self.turns)
                        actions.append('submit-' + identity)
                        done = Value(method='turn/completed', payload=Value(turn=Value(id=identity, status=Value(value='completed'))))
                        events = [done]
                        if progress:
                            events.insert(0, Value(method='item/started', payload=Value(item={'type': 'agentMessage'})))
                        self.events[identity] = iter(events)
                        return Value(turn=Value(id=identity))
                    def next_turn_notification(self, identity):
                        event = next(self.events[identity])
                        actions.append(event.method + '-' + identity)
                        return event
                    def next_notification(self):
                        return Value(method='thread/status/changed', payload=Value(threadId='root', status={'type': 'idle'}))
                    def wait_for_turn_completed(self, identity): return {'id': identity}
                    def turn_interrupt(self, *_): actions.append('interrupt'); return {}
                client_module = ModuleType('openai_codex.client')
                client_module.CodexClient = Client
                client_module.CodexConfig = lambda **kw: kw
                types_module = ModuleType('openai_codex.generated.v2_all')
                types_module.TurnStartParams = SimpleNamespace(model_validate=lambda value: value)
                model_module = ModuleType('pydantic')
                model_module.BaseModel = object
                model_module.ConfigDict = lambda **kw: kw
                definition = recipe('codex', number)
                flow = Flow(definition)
                plan = dict(definition, _peer={'command': '/unused-peer', 'args': []}, root='/unused', project='/unused', _fixtureProject='/runtime-project', model='fixture')
                modules = {'openai_codex.client': client_module, 'openai_codex.generated.v2_all': types_module, 'pydantic': model_module}
                with tempfile.TemporaryDirectory() as tmp, patch.dict('sys.modules', modules):
                    plan['root'] = tmp
                    if number == 'continuation':
                        raw = Path(tmp) / 'raw'
                        raw.mkdir()
                        frames = [
                            {'direction': 'c2s', 'wire': {'method': 'turn/start', 'params': {'threadId': 'root'}}},
                            {'direction': 's2c', 'wire': {'method': 'turn/completed', 'params': {'threadId': 'root', 'turn': {'id': '1'}}}},
                            {'direction': 's2c', 'wire': {'method': 'thread/status/changed', 'params': {'threadId': 'root', 'status': {'type': 'idle'}}}},
                        ]
                        (raw / 'raw.jsonl').write_text(''.join(json.dumps(frame) + '\n' for frame in frames))
                    codex(plan, report(), flow)
                self.assertEqual(['/runtime-project'], launch_cwds)
                self.assertEqual(['/unused'], native_cwds)
                if not progress and number in ('active-input', 'interrupt'):
                    with self.assertRaises(Untriggered): flow.finish()
                    self.assertNotIn('submit-2', actions)
                else:
                    flow.finish()
                if number == 'continuation':
                    self.assertLess(actions.index('turn/completed-1'), actions.index('submit-2'))
                if number == 'active-input' and progress:
                    self.assertLess(actions.index('item/started-1'), actions.index('submit-2'))
                    self.assertLess(actions.index('submit-2'), actions.index('turn/completed-1'))
                if number == 'interrupt' and progress:
                    self.assertEqual(actions.count('interrupt'), 1)

    def test_claude_interrupt_result_requires_acknowledged_cancellation(self):
        cases = [
            ('stream-aborted', 'interrupt', True, True, 'error_during_execution', 'aborted_streaming', None, False),
            ('tools-aborted', 'interrupt', True, True, 'error_during_execution', 'aborted_tools', None, False),
            ('no-progress', 'interrupt', False, True, 'error_during_execution', 'aborted_streaming', None, True),
            ('interrupt-rejected', 'interrupt', True, False, 'error_during_execution', 'aborted_streaming', None, True),
            ('ordinary-task', 'continuation', True, True, 'error_during_execution', 'aborted_streaming', None, True),
            ('unrelated-error', 'interrupt', True, True, 'error_during_execution', 'completed', None, True),
            ('missing-reason', 'interrupt', True, True, 'error_during_execution', None, None, True),
            ('wrong-subtype', 'interrupt', True, True, 'error_max_turns', 'aborted_streaming', None, True),
            ('api-error', 'interrupt', True, True, 'error_during_execution', 'aborted_streaming', 500, True),
        ]
        for name, number, progress, accepted, result_subtype, reason, api_status, fails in cases:
            with self.subTest(name=name):
                @dataclass
                class ResultMessage:
                    is_error: bool = True
                    terminal_reason: str = reason
                    subtype: str = result_subtype
                    api_error_status: int = api_status
                class StreamEvent: pass
                class Client:
                    def __init__(self, **_): pass
                    async def __aenter__(self): return self
                    async def __aexit__(self, *_): pass
                    async def query(self, _): pass
                    async def interrupt(self):
                        if not accepted:
                            raise ValueError('interrupt rejected')
                    async def receive_response(self):
                        if progress:
                            yield StreamEvent()
                        yield ResultMessage()
                sdk = ModuleType('claude_agent_sdk')
                sdk.ClaudeSDKClient = Client
                sdk.ClaudeAgentOptions = lambda **kw: kw
                sdk.Transport = object
                sdk.PermissionResultAllow = sdk.PermissionResultDeny = lambda **kw: kw
                sdk.ResultMessage = ResultMessage
                definition = recipe('claude', number)
                flow, saved = Flow(definition), report()
                with patch.dict('sys.modules', {'claude_agent_sdk': sdk}):
                    call = claude(dict(definition, project='/unused', model='fixture'), saved, flow)
                    if fails:
                        with self.assertRaises(ValueError):
                            asyncio.run(call)
                    else:
                        asyncio.run(call)
                        flow.finish()
                        self.assertEqual(saved['results'], 1)
                        self.assertTrue(saved['receipts'][-2]['result']['is_error'])
                        self.assertEqual(saved['receipts'][-1], {'interrupted-result': {'terminalReason': reason}})

    def test_claude_native_adapter_uses_barrier_once(self):
        for number, progress in [('continuation', True), ('active-input', True), ('active-input', False), ('interrupt', True), ('interrupt', False)]:
            with self.subTest(number=number, progress=progress):
                actions = []
                launch_cwds = []
                @dataclass
                class ResultMessage:
                    is_error: bool = False
                    session_id: str = 'root'
                class StreamEvent: pass
                class Client:
                    def __init__(self, **kwargs): launch_cwds.append(kwargs['options']['cwd'])
                    async def __aenter__(self): return self
                    async def __aexit__(self, *_): actions.append('closed')
                    async def query(self, _): actions.append('submit')
                    async def receive_messages(self):
                        yield {'subtype': 'session_state_changed', 'state': 'idle', 'session_id': 'root'}
                    async def interrupt(self): actions.append('interrupt')
                    async def receive_response(self):
                        if progress:
                            actions.append('progress')
                            yield StreamEvent()
                        actions.append('result')
                        yield ResultMessage()
                sdk = ModuleType('claude_agent_sdk')
                sdk.ClaudeSDKClient = Client
                sdk.ClaudeAgentOptions = lambda **kw: kw
                sdk.Transport = object
                sdk.PermissionResultAllow = sdk.PermissionResultDeny = lambda **kw: kw
                sdk.ResultMessage = ResultMessage
                definition = recipe('claude', number)
                flow = Flow(definition)
                with patch.dict('sys.modules', {'claude_agent_sdk': sdk}):
                    asyncio.run(claude(dict(definition, project='/unused', _fixtureProject='/runtime-project', model='fixture'), report(), flow))
                self.assertEqual(['/runtime-project'], launch_cwds)
                if not progress and number in ('active-input', 'interrupt'):
                    with self.assertRaises(Untriggered): flow.finish()
                    self.assertEqual(actions.count('submit'), 1)
                else:
                    flow.finish()
                if number == 'continuation':
                    self.assertEqual(actions, ['submit', 'progress', 'result', 'submit', 'progress', 'result', 'closed'])
                if number == 'active-input' and progress:
                    self.assertEqual(actions[:4], ['submit', 'progress', 'submit', 'result'])
                if number == 'interrupt' and progress:
                    self.assertEqual(actions.count('interrupt'), 1)


if __name__ == '__main__':
    unittest.main()
