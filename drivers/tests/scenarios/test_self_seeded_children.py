import copy
import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from record_user_story.self_seeded_children import require_setup_wire, run, verify_setup


PARENT = '12345678-1234-4234-8234-123456789012'
CHILD = '87654321-4321-4321-8321-210987654321'


def codex_setup():
    rows = []
    def send(method, params, result=None):
        identity = str(len(rows))
        rows.append({'direction': 'c2s', 'wire': {'id': identity, 'method': method, 'params': params}})
        if result is not None:
            rows.append({'direction': 's2c', 'wire': {'id': identity, 'result': result}})
    def event(method, params):
        rows.append({'direction': 's2c', 'wire': {'method': method, 'params': params}})
    send('initialize', {}, {})
    send('initialized', {})
    send('thread/start', {}, {'thread': {'id': PARENT}, 'model': 'test-model'})
    send('turn/start', {'threadId': PARENT, 'input': [{'type': 'text', 'text': 'setup-prompt'}]},
         {'turn': {'id': 'root-turn'}})
    for kind in ('started', 'completed'):
        if kind == 'completed':
            event('turn/completed', {'threadId': CHILD, 'turn': {'id': 'child-turn', 'status': 'completed'}})
        event('item/completed', {'threadId': PARENT,
              'item': {'type': 'subAgentActivity', 'kind': kind, 'agentThreadId': CHILD}})
    event('turn/completed', {'threadId': PARENT, 'turn': {'id': 'root-turn', 'status': 'completed'}})
    send('thread/unsubscribe', {'threadId': PARENT}, {'status': 'unsubscribed'})
    return rows


def claude_setup():
    return [
        {'direction': 'c2s', 'wire': {'type': 'user', 'session_id': '',
                                    'message': {'content': 'setup-prompt'}}},
        {'direction': 's2c', 'wire': {'type': 'system', 'subtype': 'task_started',
            'session_id': PARENT, 'task_id': 'child', 'tool_use_id': 'tool', 'run_id': 'run'}},
        {'direction': 's2c', 'wire': {'type': 'system', 'subtype': 'task_notification',
            'session_id': PARENT, 'task_id': 'child', 'tool_use_id': 'tool', 'run_id': 'run', 'status': 'completed'}},
        {'direction': 's2c', 'wire': {'type': 'result', 'subtype': 'success',
                                    'session_id': PARENT, 'is_error': False}},
    ]


class SelfSeededChildrenTests(unittest.TestCase):
    def test_setup_material_is_self_contained_and_hashes_native_identities(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'raw').mkdir()
            (root / 'existing-children').mkdir()
            raw = root / 'raw/raw.jsonl'
            raw.write_text('\n'.join(json.dumps(row) for row in codex_setup()) + '\n')
            transport = root / 'raw/transport.json'
            transport.write_text(json.dumps({'captureStatus': 'complete', 'nativeExitCode': 0,
                'nativeLaunched': True, 'clientEOF': True, 'reaped': True, 'cleanupComplete': True}))
            digest = lambda path: hashlib.sha256(path.read_bytes()).hexdigest()
            bindings = []
            for identity in (PARENT, CHILD):
                target = 'config/sessions/' + identity + '.jsonl'
                source = 'history/seed/' + target
                native = root / source
                native.parent.mkdir(parents=True, exist_ok=True)
                payload = {'id': identity, 'cwd': '/recorded'}
                binding = {'role': 'seed', 'sessionId': identity, 'project': '/recorded',
                           'nativeTarget': target, 'source': source}
                if identity == CHILD:
                    payload['parent_thread_id'] = PARENT
                    binding.update(parentSessionId=PARENT, childSessionId=CHILD)
                native.write_text(json.dumps({'type': 'session_meta', 'payload': payload}) + '\n')
                bindings.append(dict(binding, sha256=digest(native)))
            setup = {'schemaVersion': 1, 'provider': 'codex', 'sessionId': PARENT, 'agentIds': [CHILD],
                'historyBindings': bindings, 'rawSHA256': digest(raw), 'transportSHA256': digest(transport)}
            path = root / 'existing-children/setup.json'
            path.write_text(json.dumps(setup))
            plan = {'provider': 'codex', 'model': 'test-model',
                    'scenario': {'parameters': {'setupPrompt': 'setup-prompt'}}}
            self.assertEqual(verify_setup(plan, root), setup)
            child_path = root / bindings[1]['source']
            original = child_path.read_bytes()
            for variant in ('hash', 'uuid', 'parent', 'project', 'escape', 'source-case', 'transport'):
                broken = copy.deepcopy(setup)
                child_path.write_bytes(original)
                if variant == 'hash': child_path.write_bytes(original + b'\n')
                if variant == 'uuid': broken['sessionId'] = 'invalid'
                if variant == 'parent': broken['historyBindings'][1]['parentSessionId'] = CHILD
                if variant == 'project': broken['historyBindings'][1]['project'] = '/different'
                if variant == 'escape': broken['historyBindings'][1]['source'] = '../outside'
                if variant == 'source-case': broken['historyBindings'][1]['source'] = 'history/seed/source/material/' + bindings[1]['nativeTarget']
                if variant == 'transport': broken['transportSHA256'] = '0' * 64
                path.write_text(json.dumps(broken))
                with self.subTest(variant=variant), self.assertRaises(ValueError):
                    verify_setup(plan, root)

    def test_setup_requires_native_child_completion_before_parent_detach(self):
        plan = {'provider': 'codex', 'model': 'test-model',
                'scenario': {'parameters': {'setupPrompt': 'setup-prompt'}}}
        setup = {'sessionId': PARENT, 'agentIds': [CHILD]}
        require_setup_wire(plan, codex_setup(), setup)
        for variant in ('child-failed', 'parent-failed', 'late-child', 'wrong-parent', 'wrong-model',
                        'extra-input', 'missing-detach', 'early-input', 'wrong-prompt'):
            rows = copy.deepcopy(codex_setup())
            if variant == 'child-failed':
                rows[8]['wire']['params']['turn']['status'] = 'failed'
            if variant == 'parent-failed':
                rows[10]['wire']['params']['turn']['status'] = 'failed'
            if variant == 'late-child':
                rows[8], rows[10] = rows[10], rows[8]
            if variant == 'wrong-parent':
                rows[5]['wire']['params']['threadId'] = CHILD
            if variant == 'wrong-model':
                rows[4]['wire']['result']['model'] = 'different-model'
            if variant == 'extra-input':
                rows.append(copy.deepcopy(rows[5]))
            if variant == 'missing-detach':
                rows = rows[:-2]
            if variant == 'early-input':
                rows[4], rows[5] = rows[5], rows[4]
            if variant == 'wrong-prompt':
                rows[5]['wire']['params']['input'][0]['text'] = 'different'
            with self.subTest(variant=variant), self.assertRaises(ValueError):
                require_setup_wire(plan, rows, setup)

    def test_claude_requires_same_run_child_completion_before_successful_parent(self):
        plan = {'provider': 'claude', 'scenario': {'parameters': {'setupPrompt': 'setup-prompt'}}}
        setup = {'sessionId': PARENT, 'agentIds': ['child']}
        require_setup_wire(plan, claude_setup(), setup)
        for variant in ('failed', 'late', 'other-child', 'other-parent', 'other-run', 'other-tool', 'extra-input'):
            rows = copy.deepcopy(claude_setup())
            if variant == 'failed': rows[3]['wire']['is_error'] = True
            if variant == 'late': rows[2], rows[3] = rows[3], rows[2]
            if variant == 'other-child': rows[2]['wire']['task_id'] = 'another'
            if variant == 'other-parent': rows[2]['wire']['session_id'] = CHILD
            if variant == 'other-run': rows[2]['wire']['run_id'] = 'another'
            if variant == 'other-tool': rows[2]['wire']['tool_use_id'] = 'another'
            if variant == 'extra-input': rows.append(copy.deepcopy(rows[0]))
            with self.subTest(variant=variant), self.assertRaises(ValueError):
                require_setup_wire(plan, rows, setup)

    def test_setup_failure_cannot_start_observation_or_become_success(self):
        with patch('record_user_story.self_seeded_children.setup_codex', side_effect=ValueError('setup failed')), \
                patch('codex_driver.existing.observe') as observe:
            with self.assertRaisesRegex(ValueError, 'setup failed'):
                run({'provider': 'codex'}, {'events': 0})
            observe.assert_not_called()
