"""Offline process checks for an independently installed tool (no Agent calls)."""
import os
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class IndependentToolTests(unittest.TestCase):
    def test_recordings_cases_and_pending_sdk_assertions_keep_imported_bytes(self):
        baseline = json.loads((ROOT / 'docs/import-baseline.json').read_text())
        for entry in baseline['files']:
            if entry['path'].startswith(('tests/simulation/recordings/', 'tests/simulation/scenarios/')):
                with self.subTest(path=entry['path']):
                    self.assertEqual(entry['sha256'], hashlib.sha256((ROOT / entry['path']).read_bytes()).hexdigest())
        migration = json.loads((ROOT / 'migration/agent-sdk-go/manifest.json').read_text())
        imported = {entry['path']: entry for entry in baseline['files']}
        for entry in migration['files']:
            with self.subTest(path=entry['pendingPath']):
                self.assertEqual(imported[entry['sourcePath']]['sha256'], entry['sha256'])
                self.assertEqual(entry['sha256'], hashlib.sha256((ROOT / entry['pendingPath']).read_bytes()).hexdigest())

    def test_repository_build_does_not_require_sdk_driver(self):
        with tempfile.TemporaryDirectory() as temporary:
            environment = dict(os.environ, AGENT_SDK_TESTIFY_BUILD_DIR=temporary,
                               PYTHON='python3.11', GOPROXY='off', GOSUMDB='off',
                               PIP_NO_INDEX='1', GOWORK='off')
            result = subprocess.run(['bash', str(ROOT / 'scripts/simulation.sh'), 'build'],
                                    cwd=temporary, env=environment, capture_output=True,
                                    text=True, timeout=120)
            self.assertEqual(0, result.returncode, result.stdout + result.stderr)
            self.assertTrue((Path(temporary) / 'agent-sdk-testify').is_file())
            self.assertEqual(1, len(list(Path(temporary).glob('*.whl'))))
            self.assertFalse((Path(temporary) / 'sdk-driver').exists())

    def test_default_tests_run_without_sdk_or_binary_override(self):
        environment = dict(os.environ)
        environment.pop('AGENT_SDK_TESTIFY_BIN', None)
        environment.update(GOPROXY='off', GOSUMDB='off', GOWORK='off')
        result = subprocess.run(['go', 'test', './...', '-run', '^TestBinaryUsesItselfForCaptureReplayAndSimulation$'],
                                cwd=ROOT, env=environment, capture_output=True, text=True, timeout=120)
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        dependencies = subprocess.check_output(['go', 'list', '-deps', '-test', './...'],
                                               cwd=ROOT, env=environment, text=True)
        self.assertNotIn('github.com/zoumo/agent-sdk-go', dependencies)

    def test_installed_binary_has_independent_module_identity(self):
        with tempfile.TemporaryDirectory() as temporary:
            binary = Path(temporary) / 'agent-sdk-testify'
            subprocess.run(['go', 'build', '-o', str(binary), '.'],
                           cwd=ROOT, check=True, capture_output=True)
            identity = subprocess.check_output(['go', 'version', '-m', str(binary)], text=True)
            self.assertIn('path\tgithub.com/zoumo/agent-sdk-testify\n', identity)
            self.assertNotIn('github.com/zoumo/agent-sdk-go', identity)
            result = subprocess.run([str(binary), 'version'], cwd=temporary,
                                    env={'PATH': '/missing', 'HOME': temporary},
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(0, result.returncode, result.stderr)
            self.assertIn('0.1.0', result.stdout)


if __name__ == '__main__':
    unittest.main()
