import copy
import json
from pathlib import Path
import shutil
import tempfile
import unittest

from record_user_story.existing_children import verify_seed
from test_canonical_seed import digest, inventory


class LegacyCanonicalSeedTests(unittest.TestCase):
    def prepare(self, root, provider):
        frozen = root / 'frozen'
        source = frozen / 'source'
        source.mkdir(parents=True)
        (frozen / 'project').mkdir()
        project = '/old/project'
        saved = {}
        for identity in ('parent', 'child'):
            if provider == 'codex':
                target = f'config/sessions/rollout-{identity}.jsonl'
                payload = {'id': identity, 'cwd': project, 'source': 'vscode'}
                if identity == 'child':
                    payload['source'] = {'subagent': {'thread_spawn': {'parent_thread_id': 'parent'}}}
                row = {'type': 'session_meta', 'payload': payload}
            else:
                target = 'config/projects/-old-project/' + ('parent.jsonl' if identity == 'parent'
                         else 'parent/subagents/agent-child.jsonl')
                row = {'sessionId': 'parent', 'cwd': project}
                if identity == 'child': row['agentId'] = 'child'
            path = source / target
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(json.dumps(row) + '\n')
            saved[target] = digest(path)
            if provider == 'claude' and identity == 'child':
                sidecar = path.with_suffix('.meta.json')
                sidecar.write_text('{"agentType":"fixture"}\n')
                saved[sidecar.relative_to(source).as_posix()] = digest(sidecar)
        previous = {'provider': provider, 'project': project, 'driverProfile': 'history',
                    'scenario': {'parameters': {'historyTarget': 'child-messages'}}}
        (source / 'plan.json').write_text(json.dumps(previous))
        (source / 'manifest.json').write_text(json.dumps({'planSHA256': digest(source / 'plan.json'),
                                                         'files': {}, 'savedFiles': saved}))
        prefix = 'history/seed/source/'
        seed = {'provider': provider, 'sourceProject': project, 'sessionId': 'parent', 'agentIds': ['child'],
                'materialPlan': {'path': prefix + 'plan.json', 'sha256': digest(source / 'plan.json')},
                'sourceFiles': inventory(frozen), 'projectFiles': {},
                'files': {name: {'source': prefix + name, 'sha256': sha}
                          for name, sha in saved.items() if name.endswith('.jsonl')}}
        plan = {'provider': provider, 'historyProject': project, 'historySeed': seed,
                'args': ['--resume', 'parent'] if provider == 'claude' else [],
                '_runtime': {'seedRoot': str(frozen), 'replay': False}}
        self.assertIs(verify_seed(plan), seed)
        material = root / 'material'
        shutil.copytree(frozen, material / 'history/seed')
        replay = copy.deepcopy(plan)
        replay['_runtime'] = {'seedRoot': str(material), 'replay': True}
        for key in ('files', 'sourceFiles', 'projectFiles'): replay['historySeed'].pop(key)
        replay['historyBindings'] = []
        for name, sha in saved.items():
            child = '/subagents/' in name or name.endswith('-child.jsonl')
            binding = {'role': 'seed', 'sessionId': 'child' if child else 'parent', 'project': project,
                       'nativeTarget': name, 'source': prefix + name, 'sha256': sha}
            if child: binding.update(parentSessionId='parent', childSessionId='child')
            replay['historyBindings'].append(binding)
        shutil.rmtree(frozen)
        return replay, material

    def seal(self, plan, material):
        (material / 'plan.json').write_text(json.dumps({k: v for k, v in plan.items() if k != '_runtime'}))
        files = inventory(material)
        files.pop('manifest.json', None)
        (material / 'manifest.json').write_text(json.dumps({'materialFormatVersion': 3,
            'planSHA256': digest(material / 'plan.json'), 'files': files}))

    def test_legacy_record_and_sealed_replay(self):
        for provider in ('codex', 'claude'):
            with self.subTest(provider=provider), tempfile.TemporaryDirectory() as directory:
                plan, material = self.prepare(Path(directory), provider)
                self.seal(plan, material)
                original = copy.deepcopy(plan)
                verified = verify_seed(plan)
                self.assertEqual(original, plan)
                self.assertEqual(2, len(verified['files']))
                self.assertNotIn('files', plan['historySeed'])
                # A changed runtime file may replace a binding; the frozen
                # original and its own native identity must remain verifiable.
                changed = copy.deepcopy(plan)
                binding = changed['historyBindings'][0]
                data = (material / binding['source']).read_bytes()
                binding.update(role='runtime', source='history/runtime/' + binding['nativeTarget'])
                path = material / binding['source']
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(data + b'{}\n')
                binding['sha256'] = digest(path)
                self.seal(changed, material)
                verify_seed(changed)

    def test_rejects_resealed_identity_binding_and_source_tampering(self):
        for provider in ('codex', 'claude'):
            for failure in ('provider', 'parent', 'children', 'hash', 'binding', 'binding-source',
                            'binding-parent', 'binding-project', 'target', 'duplicate', 'role',
                            'escape', 'source-tamper', 'symlink'):
                with self.subTest(provider=provider, failure=failure), tempfile.TemporaryDirectory() as directory:
                    plan, material = self.prepare(Path(directory), provider)
                    seed, bindings = plan['historySeed'], plan['historyBindings']
                    if failure == 'provider': seed['provider'] = 'other'
                    if failure == 'parent': seed['sessionId'] = 'other'
                    if failure == 'children': seed['agentIds'] = ['other']
                    if failure == 'hash': seed['materialPlan']['sha256'] = '0' * 64
                    if failure == 'binding': bindings[0]['sha256'] = '0' * 64
                    if failure == 'binding-source': bindings[0]['source'] = bindings[1]['source']
                    if failure == 'binding-parent': bindings[1]['parentSessionId'] = 'other'
                    if failure == 'binding-project': bindings[0]['project'] = '/other'
                    if failure == 'target': bindings[0]['nativeTarget'] += '.other'
                    if failure == 'duplicate': bindings.append(copy.deepcopy(bindings[0]))
                    if failure == 'role': bindings[0]['role'] = 'other'
                    if failure == 'escape': seed['materialPlan']['path'] = 'history/seed/source/../plan.json'
                    path = material / bindings[0]['source']
                    if failure == 'source-tamper': path.write_text('{}\n')
                    self.seal(plan, material)
                    if failure == 'symlink':
                        moved = Path(directory) / 'outside'
                        path.rename(moved)
                        path.symlink_to(moved)
                    with self.assertRaises(ValueError): verify_seed(plan)
