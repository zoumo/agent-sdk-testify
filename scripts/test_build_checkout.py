"""Offline regression checks for exact-commit builds from Git worktree files."""
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import tempfile
import unittest

WRAPPER = Path(__file__).with_name('with-build-checkout.sh')


class BuildCheckoutTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='testify build checkout ')
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.source = self.root / 'original'
        self.source.mkdir()
        (self.source / 'scripts').mkdir()
        shutil.copyfile(WRAPPER, self.source / 'scripts' / WRAPPER.name)
        shutil.copyfile(WRAPPER.parent / 'check-offline.sh', self.source / 'scripts' / 'check-offline.sh')
        shutil.copyfile(WRAPPER.parent.parent / 'Makefile', self.source / 'Makefile')
        (self.source / '.gitignore').write_text('/.artifacts/\n/bin/\n')
        (self.source / 'go.mod').write_text('module example.test/buildcheckout\n\ngo 1.26.0\n')
        (self.source / 'internal' / 'marker').mkdir(parents=True)
        (self.source / 'internal' / 'marker' / 'marker.go').write_text('package marker\n')
        (self.source / 'identity_test.go').write_text('''package buildcheckout_test
import ("runtime/debug"; "testing")
func TestIdentity(t *testing.T) {
    info, ok := debug.ReadBuildInfo()
    if !ok { t.Fatal("missing build info") }
    for _, setting := range info.Settings {
        if setting.Key == "vcs.revision" && setting.Value != "" { return }
    }
    t.Fatal("missing VCS revision")
}
''')
        self.git('init', '-q')
        self.git('add', '.')
        self.git('-c', 'user.name=Build Checkout Test', '-c', 'user.email=build@example.invalid',
                 'commit', '-qm', 'synthetic source')
        self.head = self.git('rev-parse', 'HEAD').stdout.strip()
        self.tree = self.git('rev-parse', 'HEAD^{tree}').stdout.strip()

    def git(self, *args):
        return subprocess.run(['git', *args], cwd=self.source, text=True, check=True,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    def use_git_file(self):
        original = self.source
        self.source = self.root / 'linked source'
        subprocess.run(['git', 'clone', '--quiet', '--no-hardlinks',
                        '--separate-git-dir', str(self.root / 'git metadata'),
                        str(original), str(self.source)], check=True, capture_output=True)
        self.assertTrue((self.source / '.git').is_file())

    def invoke(self, *args):
        return subprocess.run(['bash', str(self.source / 'scripts' / WRAPPER.name), *args],
                              cwd=self.root, text=True, capture_output=True,
                              env=dict(os.environ, TMPDIR=str(self.root)), timeout=120)

    def test_directory_checkout_runs_in_place(self):
        result = self.invoke('pwd', '-P')
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertEqual(self.source.resolve(), Path(result.stdout.strip()))
        self.assertNotIn('Build checkout:', result.stderr)
        self.assertEqual([], list(self.root.glob('agent-sdk-testify-build.*')))

    def test_git_file_builds_the_exact_source_with_real_vcs_info(self):
        self.use_git_file()
        result = subprocess.run(['make', 'test', 'GO_TEST_FLAGS=-buildvcs=true -count=1'],
                                cwd=self.source, text=True, capture_output=True,
                                env=dict(os.environ, TMPDIR=str(self.root)), timeout=120)
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        self.assertIn('Build revision: ' + self.head, result.stderr)
        self.assertIn('Build tree: ' + self.tree, result.stderr)
        self.assertEqual([], list(self.root.glob('agent-sdk-testify-build.*')))
        self.assertTrue((self.source / '.git').is_file())
        self.assertEqual(self.head, self.git('rev-parse', 'HEAD').stdout.strip())
        self.assertEqual('', self.git('status', '--porcelain=v1').stdout)

    def test_dirty_source_is_rejected_before_running_command(self):
        self.use_git_file()
        for path in ['go.mod', 'untracked']:
            with self.subTest(path=path):
                target = self.source / path
                previous = target.read_bytes() if target.exists() else None
                target.write_text('dirty\n')
                marker = self.root / 'must not run'
                result = self.invoke('touch', str(marker))
                self.assertNotEqual(0, result.returncode)
                self.assertIn('source is dirty', result.stderr)
                self.assertFalse(marker.exists())
                self.assertEqual([], list(self.root.glob('agent-sdk-testify-build.*')))
                if previous is None:
                    target.unlink()
                else:
                    target.write_bytes(previous)

    def test_failure_preserves_exit_code_checkout_and_command_log(self):
        self.use_git_file()
        result = self.invoke('bash', '-c', 'exit 17')
        self.assertEqual(17, result.returncode)
        retained = list(self.root.glob('agent-sdk-testify-build.*'))
        self.assertEqual(1, len(retained))
        self.assertIn(str(retained[0]), result.stderr)
        self.assertTrue((retained[0] / 'source' / '.git').is_dir())
        self.assertIn('exit=17', (retained[0] / 'command.log').read_text())

    def test_unreadable_git_status_cannot_be_treated_as_clean_source(self):
        self.use_git_file()
        commands = self.root / 'commands'
        commands.mkdir()
        shim = commands / 'git'
        shim.write_text('#!/bin/sh\nif [ "$1" = status ]; then exit 19; fi\nexec ' +
                        shlex.quote(shutil.which('git')) + ' "$@"\n')
        shim.chmod(0o700)
        marker = self.root / 'must not run'
        result = subprocess.run(['bash', str(self.source / 'scripts' / WRAPPER.name),
                                 'touch', str(marker)], text=True, capture_output=True, timeout=120,
                                env=dict(os.environ, TMPDIR=str(self.root),
                                         PATH=str(commands) + os.pathsep + os.environ['PATH']))
        self.assertEqual(19, result.returncode)
        self.assertFalse(marker.exists())
        self.assertEqual([], list(self.root.glob('agent-sdk-testify-build.*')))

    def test_absolute_artifact_output_survives_successful_cleanup(self):
        self.use_git_file()
        output = self.root / 'output with spaces'
        result = self.invoke('bash', '-c', 'printf artifact > "$1"', '_', str(output))
        self.assertEqual(0, result.returncode, result.stderr)
        self.assertEqual('artifact', output.read_text())
        self.assertEqual([], list(self.root.glob('agent-sdk-testify-build.*')))

    def test_successful_command_cannot_change_the_source_checkout(self):
        self.use_git_file()
        result = self.invoke('bash', '-c', 'printf changed >> go.mod')
        self.assertEqual(1, result.returncode)
        self.assertIn('changed the independent source checkout', result.stderr)
        self.assertEqual(1, len(list(self.root.glob('agent-sdk-testify-build.*'))))
        self.assertEqual('', self.git('status', '--porcelain=v1').stdout)

    def test_offline_entry_preserves_artifact_location_and_does_not_clone_per_phase(self):
        self.use_git_file()
        result = subprocess.run(['bash', str(self.source / 'scripts' / 'check-offline.sh'), 'unknown'],
                                cwd=self.root, text=True, capture_output=True, timeout=120,
                                env=dict(os.environ, TMPDIR=str(self.root),
                                         ARTIFACT_DIR='.artifacts/located artifacts'))
        self.assertEqual(2, result.returncode)
        self.assertEqual(1, result.stderr.count('Build checkout:'))
        self.assertTrue((self.source / '.artifacts' / 'located artifacts' / 'phase-times.tsv').exists())
        self.assertEqual(1, len(list(self.root.glob('agent-sdk-testify-build.*'))))


if __name__ == '__main__':
    unittest.main()
