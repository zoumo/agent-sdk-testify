"""The attempt-local runtime configuration is validated before any launch."""
import json
from pathlib import Path
import tempfile
import unittest

from record_user_story.driver import main
from record_user_story.runtime_config import (
    apply_runtime,
    execution_workspace,
    protocol_project,
)


def runtime(root, **updates):
    value = {
        'schemaVersion': 1,
        'root': str(root),
        'workspace': str(root / 'executions' / 'e1' / 'workspace'),
        'home': str(root / 'executions' / 'e1' / 'home'),
        'config': str(root / 'executions' / 'e1' / 'config'),
        'raw': str(root / 'raw'),
        'tmp': str(root / 'tmp'),
        'output': str(root),
        'replay': True,
        'peer': {'command': '/absolute/peer', 'args': ['record']},
        'agentResults': {'main': str(root / 'result.json')},
    }
    value.update(updates)
    return value


class RuntimeConfigTests(unittest.TestCase):

    def test_runtime_is_separate_from_material_identity(self):
        root = Path(tempfile.mkdtemp())
        material = {
            'caseId': 'SDK-CASE-001',
            'project': '/recorded/project',
            'historyProject': '/recorded/history',
            '_fixtureProject': '/material/workspace',
        }
        with tempfile.NamedTemporaryFile('w', suffix='.json', delete=False) as out:
            out.write(json.dumps(runtime(root)))
            path = out.name
        request = {'case': 'SDK-CASE-001', 'materials': str(root), 'runtime': path}
        loaded = apply_runtime(material, request)
        self.assertEqual(loaded['root'], str(root))
        self.assertEqual(execution_workspace(material), root / 'executions' / 'e1' / 'workspace')
        self.assertEqual(protocol_project(material), Path('/material/workspace'))
        material.pop('_fixtureProject')
        self.assertEqual(protocol_project(material), Path('/recorded/project'))

    def test_unknown_or_invalid_runtime_fails_closed(self):
        root = Path(tempfile.mkdtemp())
        for name, value, message in [
            ('unknown version', runtime(root, schemaVersion=2), 'unsupported'),
            ('missing version', runtime(root, schemaVersion=None), 'unsupported'),
            ('relative workspace', runtime(root, workspace='workspace'), 'absolute workspace'),
            ('missing raw', runtime(root, raw=''), 'absolute raw'),
            ('missing replay', runtime(root, replay=None), 'requires replay'),
            ('missing peer', runtime(root, peer=None), 'peer is missing'),
            ('relative peer command', runtime(root, peer={'command': 'peer'}), 'must be absolute'),
            ('bad peer args', runtime(root, peer={'command': '/peer', 'args': ['x', 1]}), 'args are invalid'),
            ('bad peer env', runtime(root, peer={'command': '/peer', 'env': {'A': 1}}), 'env is invalid'),
            ('relative fixture', runtime(root, fixtureDir='fixture'), 'fixtureDir must be absolute'),
            ('relative recorded workspace', runtime(root, recordedWorkspace='workspace'), 'recordedWorkspace must be absolute'),
            ('relative agent result', runtime(root, agentResults={'main': 'result.json'}), 'agentResults is invalid'),
        ]:
            with self.subTest(name=name):
                config = value
                if config['schemaVersion'] is None:
                    config.pop('schemaVersion')
                if config.get('replay', 'missing') is None:
                    config.pop('replay')
                if config.get('peer', 'missing') is None:
                    config.pop('peer')
                with tempfile.NamedTemporaryFile('w', suffix='.json', delete=False) as out:
                    out.write(json.dumps(config))
                    path = out.name
                material = {'caseId': 'SDK-CASE-001', 'project': '/recorded/project'}
                with self.assertRaisesRegex(ValueError, message):
                    apply_runtime(material, {'materials': str(root), 'runtime': path})

    def test_driver_rejects_bad_runtime_before_launch(self):
        root = Path(tempfile.mkdtemp())
        materials = root / 'materials'
        materials.mkdir()
        (materials / 'case.json').write_text(json.dumps({
            'caseId': 'SDK-CASE-001', 'provider': 'codex',
            'driverProfile': 'new-session', 'project': '/recorded/project',
        }))
        config = runtime(root, schemaVersion=99)
        runtime_path = root / 'runtime.json'
        runtime_path.write_text(json.dumps(config))
        request_path = root / 'request.json'
        request_path.write_text(json.dumps({
            'case': 'SDK-CASE-001', 'materials': str(materials),
            'runtime': str(runtime_path),
        }))
        self.assertEqual(main(['--request', str(request_path)], expected_provider='codex'), 0)
        result = json.loads((root / 'result.json').read_text())
        self.assertEqual(result['status'], 'failed')
        self.assertIn('unsupported official driver runtime configuration version', result['message'])
        self.assertFalse((root / 'replay-report.json').exists())


if __name__ == '__main__':
    unittest.main()
