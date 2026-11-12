import copy
import hashlib
import json
from pathlib import Path
import shutil
import tempfile
import unittest

from record_user_story.existing_children import verify_seed


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def inventory(root):
    return {p.relative_to(root).as_posix(): digest(p) for p in root.rglob('*') if p.is_file()}


class CanonicalSeedTests(unittest.TestCase):
    def test_parent_and_replay_use_bound_frozen_sources(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            frozen = root / 'frozen'
            source = frozen / 'source/material'
            source.mkdir(parents=True)
            (frozen / 'project').mkdir()
            bindings = []
            for identity in ('parent', 'child'):
                target = f'config/sessions/{identity}.jsonl'
                name = 'history/runtime/' + target
                path = source / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(json.dumps({'type': 'session_meta', 'payload': {'id': identity}}) + '\n')
                binding = {'role': 'runtime', 'sessionId': identity, 'project': '/original',
                           'nativeTarget': target, 'source': name, 'sha256': digest(path)}
                if identity == 'child':
                    binding.update(parentSessionId='parent', childSessionId='child')
                bindings.append(binding)
            previous = {'provider': 'codex', 'project': '/original', 'driverProfile': 'history',
                        'scenario': {'parameters': {'historyTarget': 'child-messages'}},
                        'historyBindings': bindings}
            (source / 'plan.json').write_text(json.dumps(previous))
            (source / 'manifest.json').write_text(json.dumps({
                'materialFormatVersion': 3, 'planSHA256': digest(source / 'plan.json'),
                'files': inventory(source)}))
            prefix = 'history/seed/source/material/'
            parent_bindings = [dict(b, role='seed', source=prefix + b['source']) for b in bindings]
            seed = {'provider': 'codex', 'sessionId': 'parent', 'agentIds': ['child'],
                    'sourceProject': '/original', 'projectFiles': {},
                    'sourceFiles': inventory(frozen),
                    'sourcePlan': {'path': prefix + 'plan.json', 'sha256': digest(source / 'plan.json')},
                    'files': {b['nativeTarget']: {'source': prefix + b['source'], 'sha256': b['sha256']}
                              for b in bindings}}
            plan = {'provider': 'codex', 'historyProject': '/original', 'historySeed': seed,
                    'historyBindings': parent_bindings, '_runtime': {'seedRoot': str(frozen), 'replay': False}}
            self.assertIs(verify_seed(plan), seed)
            for failure in ('session', 'project', 'hash', 'binding', 'escape'):
                broken = copy.deepcopy(plan)
                if failure == 'session': broken['historySeed']['sessionId'] = 'other'
                if failure == 'project': broken['historyProject'] = '/other'
                if failure == 'hash': broken['historySeed']['sourcePlan']['sha256'] = '0' * 64
                if failure == 'binding': broken['historyBindings'][0]['sha256'] = '0' * 64
                if failure == 'escape': broken['historySeed']['sourcePlan']['path'] = prefix + '../plan.json'
                with self.subTest(failure=failure), self.assertRaises(ValueError):
                    verify_seed(broken)
            material = root / 'material'
            shutil.copytree(frozen, material / 'history/seed')
            replay = copy.deepcopy(plan)
            replay['_runtime'] = {'seedRoot': str(material), 'replay': True}
            for key in ('files', 'sourceFiles', 'projectFiles'):
                replay['historySeed'].pop(key)
            (material / 'plan.json').write_text(json.dumps({k: v for k, v in replay.items() if k != '_runtime'}))
            (material / 'manifest.json').write_text(json.dumps({
                'materialFormatVersion': 3, 'planSHA256': digest(material / 'plan.json'),
                'files': inventory(material)}))
            shutil.rmtree(frozen)
            verify_seed(replay)
            saved = material / 'history/seed/source/material' / bindings[0]['source']
            original = saved.read_bytes()
            saved.write_bytes(b'changed\n')
            with self.assertRaises(ValueError): verify_seed(replay)
            saved.write_bytes(original)
            linked = material / 'history/seed/source/material' / bindings[1]['source']
            linked.unlink()
            linked.symlink_to(saved)
            with self.assertRaises(ValueError): verify_seed(replay)
