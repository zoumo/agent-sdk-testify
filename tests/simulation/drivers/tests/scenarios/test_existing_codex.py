import copy
import unittest
from codex_driver.existing import require_wire


class ExistingCodexTests(unittest.TestCase):
    def test_resume_then_discovery_requires_identity_no_execution_and_native_end(self):
        requests = [('initialize', {}), ('initialized', None),
                    ('thread/resume', {'threadId': 'parent', 'model': 'gpt-6.1-terra'}),
                    ('thread/list', {'ancestorThreadId': 'parent', 'sourceKinds': ['subAgentThreadSpawn'], 'limit': 10}),
                    ('thread/unsubscribe', {'threadId': 'parent'})]
        frames = []
        for i, (method, params) in enumerate(requests):
            frames.append({'direction': 'c2s', 'wire': {'id': i, 'method': method, 'params': params}})
            result = {}
            if method == 'thread/resume': result = {'thread': {'id': 'parent'}, 'model': 'gpt-6.1-terra'}
            if method == 'thread/list': result = {'data': [{'id': 'child', 'parentThreadId': 'parent'}], 'nextCursor': None}
            if method == 'thread/unsubscribe': result = {'status': 'unsubscribed'}
            if method != 'initialized': frames.append({'direction': 's2c', 'wire': {'id': i, 'result': result}})
        plan = {'model': 'gpt-6.1-terra', 'historySeed': {'sessionId': 'parent', 'agentIds': ['child']}}
        for variant in ('valid', 'wrong-resume', 'new-input', 'wrong-parent', 'missing-child', 'no-end', 'wrong-model', 'early-query'):
            rows = copy.deepcopy(frames)
            if variant == 'wrong-resume': rows[4]['wire']['result']['thread']['id'] = 'another'
            if variant == 'new-input': rows.insert(-2, {'direction': 'c2s', 'wire': {'method': 'turn/start'}})
            if variant == 'wrong-parent': rows[6]['wire']['result']['data'][0]['parentThreadId'] = 'another'
            if variant == 'missing-child': rows[6]['wire']['result']['data'] = []
            if variant == 'no-end': rows[6]['wire']['result'].pop('nextCursor')
            if variant == 'early-query': rows[4], rows[5] = rows[5], rows[4]
            if variant == 'wrong-model': rows[4]['wire']['result']['model'] = 'other'
            with self.subTest(variant=variant):
                if variant == 'valid': require_wire(plan, rows)
                else:
                    with self.assertRaises(ValueError): require_wire(plan, rows)

    def test_runner_codex_seed_keeps_immutable_and_native_copies_separate(self):
        import hashlib
        import json
        import tempfile
        from pathlib import Path
        from record_user_story.existing_children import verify_seed
        with tempfile.TemporaryDirectory() as name:
            base = Path(name).resolve()
            source, target = base / 'source', base / 'target'
            for p in (source / 'project', source / 'raw', target / 'config'):
                p.mkdir(parents=True)
            saved = {}
            for identity in ('parent', 'child'):
                path = source / 'config/sessions' / (identity + '.jsonl')
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(json.dumps({'type': 'session_meta', 'payload': {'id': identity}}) + '\n')
                saved[str(path.relative_to(source))] = hashlib.sha256(path.read_bytes()).hexdigest()
            (source / 'config/state_5.sqlite').write_bytes(b'not copied')
            plan = {'provider': 'codex', 'driverProfile': 'history', 'project': str(source / 'project'),
                    'scenario': {'parameters': {'historyTarget': 'child-messages'}}}
            report = {'status': 'passed', 'receipts': [{'history-query': {'method': 'thread/list',
                'params': {'parentThreadId': 'parent'}, 'response': {'data': [{'id': 'child', 'parentThreadId': 'parent'}]}}}]}
            for key, value in {'plan': plan, 'report': report, 'manifest': {'files': {}, 'savedFiles': saved},
                               'replay-validation': {'status': 'passed', 'fullConsumption': True}}.items():
                (source / (key + '.json')).write_text(json.dumps(value))
            (source / 'raw/raw.jsonl').write_text('synthetic seed fixture')
            import shutil
            for path in saved:
                for name in [Path(path), Path('history-seed') / path]:
                    destination = target / name
                    destination.parent.mkdir(parents=True, exist_ok=True)
                    shutil.copyfile(source / path, destination)
            seed = {'provider': 'codex', 'sourceRoot': str(source), 'sourceProject': str(source / 'project'),
                    'projectFiles': {}, 'sessionId': 'parent', 'agentIds': ['child'],
                    'files': {str(Path('history-seed') / path): {'source': path, 'sha256': digest} for path, digest in saved.items()},
                    'sourceFiles': {path: hashlib.sha256((source / path).read_bytes()).hexdigest() for path in
                                    ('plan.json', 'manifest.json', 'report.json', 'replay-validation.json', 'raw/raw.jsonl')}}
            current = {'root': str(target), 'historyProject': seed['sourceProject'], 'historySeed': seed}
            verify_seed(current)
            self.assertFalse((target / 'config/state_5.sqlite').exists())
            for name in saved:
                self.assertEqual((source / name).read_bytes(), (target / 'history-seed' / name).read_bytes())
            # Native working copies can append; immutable seed evidence cannot.
            (target / 'config/sessions/parent.jsonl').write_text('native append')
            verify_seed(current)
            (target / 'history-seed/config/sessions/child.jsonl').write_text('changed')
            with self.assertRaises(ValueError): verify_seed(current, check_source=False)
