import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from record_user_story.existing_children import require_observation_wire, verify_seed


class ExistingChildrenTests(unittest.TestCase):
    def test_runner_seed_pins_source_and_copied_history(self):
        with tempfile.TemporaryDirectory() as name:
            source, root = Path(name).resolve() / 'source', Path(name).resolve() / 'target'
            for path in (source / 'project', source / 'raw', root / 'project', root / 'config'):
                path.mkdir(parents=True)
            import re
            session = '12345678-1234-1234-1234-123456789012'
            key = re.sub(r'[^a-zA-Z0-9]', '-', str((source / 'project').resolve()))
            paths = [f'config/projects/{key}/{session}.jsonl',
                     f'config/projects/{key}/{session}/subagents/agent-old.jsonl']
            saved = {}
            for path in paths:
                file = source / path
                file.parent.mkdir(parents=True, exist_ok=True)
                file.write_bytes(b'{"sessionId":"' + session.encode() + b'"}\n')
                saved[path] = hashlib.sha256(file.read_bytes()).hexdigest()
            plan = {'provider': 'claude', 'project': str(source / 'project'), 'driverProfile': 'history',
                    'scenario': {'parameters': {'historyTarget': 'child-messages'}}}
            (source / 'plan.json').write_text(json.dumps(plan))
            (source / 'manifest.json').write_text(json.dumps({'files': {}, 'savedFiles': saved}))
            (source / 'report.json').write_text(json.dumps({'status': 'passed', 'receipts': [
                {'list_subagents': {'sessionId': session, 'agentIds': ['old']}}]}))
            (source / 'replay-validation.json').write_text(json.dumps({'status': 'passed', 'fullConsumption': True}))
            (source / 'raw/raw.jsonl').write_text('synthetic source bytes, not a real recording claim')
            import shutil
            for path in saved:
                target = root / path
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(source / path, target)
            seed = {'provider': 'claude', 'sourceRoot': str(source), 'sourceProject': str(source / 'project'),
                    'projectFiles': {}, 'sessionId': session, 'agentIds': ['old'],
                    'files': {path: {'source': path, 'sha256': digest} for path, digest in saved.items()},
                    'sourceFiles': {path: hashlib.sha256((source / path).read_bytes()).hexdigest() for path in
                                    ('plan.json', 'manifest.json', 'report.json', 'replay-validation.json', 'raw/raw.jsonl')}}
            target_plan = {'root': str(root), 'historySeed': seed, 'historyProject': seed['sourceProject']}
            self.assertEqual(seed['sessionId'], session)
            self.assertEqual(seed['agentIds'], ['old'])
            verify_seed(target_plan)
            self.assertEqual(len(seed['files']), 2)
            (source / 'report.json').write_text('{"status":"failed"}')
            with self.assertRaises(ValueError): verify_seed(target_plan)
            # Offline replay depends on its immutable copy and frozen source
            # hashes, not continued availability of the prior capture directory.
            verify_seed(target_plan, check_source=False)
            target_plan['historyProject'] = str(root / 'project')
            with self.assertRaises(ValueError): verify_seed(target_plan, check_source=False)

    def test_requires_native_observed_parent_and_no_new_input(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            (root / 'raw').mkdir()
            hook = {'type': 'system', 'subtype': 'hook_started', 'hook_name': 'SessionStart:resume',
                    'session_id': 'parent', 'uuid': 'native-hook'}
            base = [{'direction': 'c2s', 'wire': {'type': 'control_request', 'request_id': 'init', 'request': {'subtype': 'initialize'}}},
                    {'direction': 's2c', 'wire': hook}]
            plan = {'root': name, 'historySeed': {'sessionId': 'parent'}}
            report = {'receipts': [{'existing-observation': {'subtype': 'hook_started', 'data': hook}}]}
            for failure in ('valid', 'new-user', 'wrong-parent', 'missing-hook', 'duplicate-hook'):
                rows = json.loads(json.dumps(base))
                if failure == 'new-user': rows.append({'direction': 'c2s', 'wire': {'type': 'user'}})
                if failure == 'wrong-parent': rows[1]['wire']['session_id'] = 'another-parent'
                if failure == 'missing-hook': rows.pop()
                if failure == 'duplicate-hook': rows.append(rows[1])
                (root / 'raw/raw.jsonl').write_text('\n'.join(json.dumps(row) for row in rows))
                with self.subTest(failure=failure):
                    if failure == 'valid': require_observation_wire(plan, report)
                    else:
                        with self.assertRaises(ValueError): require_observation_wire(plan, report)

    def test_seed_copy_is_immutable_and_cannot_escape(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            child = root / 'config/projects/key/parent/subagents/agent-child.jsonl'
            child.parent.mkdir(parents=True)
            child.write_bytes(b'{"type":"assistant"}\n')
            digest = hashlib.sha256(child.read_bytes()).hexdigest()
            key = str(child.relative_to(root))
            seed = {'provider': 'claude', 'sourceRoot': str(root.resolve()),
                    'sourceProject': str(root.resolve() / 'project'), 'files': {key: {'source': key, 'sha256': digest}}}
            plan = {'root': name, 'historySeed': seed, 'historyProject': seed['sourceProject']}
            self.assertIs(verify_seed(plan, check_source=False), seed)
            child.write_bytes(b'{"type":"user"}\n')
            with self.assertRaises(ValueError): verify_seed(plan, check_source=False)
            seed['files'] = {'config/projects/../../escape': {'sha256': digest}}
            with self.assertRaises(ValueError): verify_seed(plan, check_source=False)
