"""Offline process checks for an independently installed tool (no Agent calls)."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


def exact_version():
    result = subprocess.run(['git', 'describe', '--tags', '--exact-match', 'HEAD'],
                            cwd=ROOT, capture_output=True, text=True)
    return result.stdout.strip() if result.returncode == 0 else 'devel'


class IndependentToolTests(unittest.TestCase):
    def test_repository_build_does_not_require_sdk_driver(self):
        with tempfile.TemporaryDirectory() as temporary:
            environment = dict(os.environ, AGENT_SDK_TESTIFY_BUILD_DIR=temporary,
                               PYTHON=os.environ.get('PYTHON', 'python3'), GOPROXY='off', GOSUMDB='off',
                               PIP_NO_INDEX='1', GOWORK='off')
            result = subprocess.run(['bash', str(ROOT / 'scripts/build.sh')],
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
        result = subprocess.run(['go', 'test', '-buildvcs=true', '.', './internal/...'],
                                cwd=ROOT, env=environment, capture_output=True, text=True, timeout=120)
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        dependencies = subprocess.check_output(['go', 'list', '-deps', '-test', './...'],
                                               cwd=ROOT, env=environment, text=True)
        self.assertNotIn('github.com/zoumo/agent-sdk-go', dependencies)

    def test_installed_binary_has_repository_package_identity(self):
        with tempfile.TemporaryDirectory() as temporary:
            binary = Path(os.environ.get('AGENT_SDK_TESTIFY_BIN', str(Path(temporary) / 'agent-sdk-testify')))
            if not os.environ.get('AGENT_SDK_TESTIFY_BIN'):
                subprocess.run(['go', 'build', '-o', str(binary), './cmd/agent-sdk-testify'],
                               cwd=ROOT, check=True, capture_output=True)
            identity = subprocess.check_output(['go', 'version', '-m', str(binary)], text=True)
            self.assertIn('path\tgithub.com/zoumo/agent-sdk-testify/cmd/agent-sdk-testify\n', identity)
            self.assertNotIn('github.com/zoumo/agent-sdk-go', identity)
            result = subprocess.run([str(binary), 'version'], cwd=temporary,
                                    env={'PATH': '/missing', 'HOME': temporary},
                                    capture_output=True, text=True, timeout=10)
            self.assertEqual(0, result.returncode, result.stderr)
            self.assertEqual('agent-sdk-testify '+exact_version()+'\n', result.stdout)


if __name__ == '__main__':
    unittest.main()
