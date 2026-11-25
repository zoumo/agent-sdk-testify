#!/usr/bin/env python3
"""Install and check the official driver artifacts outside the source checkout."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import hashlib
import tarfile


def run(root, environment, *args):
    return subprocess.run([str(x) for x in args], cwd=root, env=environment,
                          capture_output=True, check=True, timeout=90, text=True)


def check_codex(archive, root):
    prefix = root / 'bin'
    prefix.mkdir()
    binary, driver = prefix / 'agent-sdk-testify', prefix / 'agent-sdk-testify-codex'
    with tarfile.open(archive) as tar:
        for path in (binary, driver):
            entry = tar.getmember(path.name)
            assert entry.isfile() and entry.mode == 0o755
            path.write_bytes(tar.extractfile(entry).read())
            path.chmod(entry.mode)
        manifest = json.load(tar.extractfile('builtin-manifest.json'))
    environment = {'PATH': str(prefix), 'HOME': str(root)}
    version = run(root, environment, binary, 'version').stdout.strip().split()[-1]
    assert archive.name.startswith(f'agent-sdk-testify_{version}_')
    driver_hash = hashlib.sha256(driver.read_bytes()).hexdigest()
    cases = [row for row in manifest['coverage'] if row['provider'] == 'codex' and row['available']]
    assert cases
    for row in cases:
        with tempfile.TemporaryDirectory(dir=root) as work:
            fixture = Path(work) / 'fixture'
            run(work, environment, binary, 'prepare', '--agent', 'codex', '--case', row['caseId'],
                '--fixture-dir', fixture)
            run(work, environment, binary, 'official', 'verify', '--fixture-dir', fixture, '--timeout', '60s')
            verification, = fixture.glob('packages/*/verify/*/verification.json')
            result = json.loads(verification.read_text())
            assert result['status'] == 'passed' and result['cleanupComplete']
            assert result['driver'] == {'path': str(driver), 'sha256': driver_hash}
            assert result['material']['sha256'] == row['materialSHA256']
            report = json.loads(verification.with_name('replay-report.json').read_text())
            outcome = json.loads(verification.with_name('driver-result.json').read_text())
            assert report['officialSDK'] == outcome['sdk'] == row['officialSDK']
            print(f"{row['caseId']} passed with {driver.name}", flush=True)
    return binary


def check_claude(tarball, binary, root):
    node, npm = shutil.which('node'), shutil.which('npm')
    assert node and npm, 'Node.js and npm are required'
    tools = root / 'tools'
    tools.mkdir()
    (tools / 'node').symlink_to(Path(node).resolve(strict=True))
    prefix = root / 'claude'
    env = {'PATH': str(tools) + os.pathsep + os.defpath, 'HOME': str(root), 'npm_config_cache': str(root / 'cache'),
           'npm_config_update_notifier': 'false'}
    run(root, env, npm, 'install', '--offline', '--ignore-scripts',
        '--omit=optional', '--no-audit', '--no-fund', '--prefix', prefix, tarball)

    bin_dir = prefix / 'node_modules/.bin'
    env['PATH'] = str(bin_dir) + os.pathsep + str(tools)
    command = 'agent-sdk-testify-claude'
    entry = bin_dir / command
    assert shutil.which(command, path=env['PATH']) == str(entry)
    installed = prefix / 'node_modules' / command
    assert entry.resolve(strict=True) == installed / 'dist/cli.js'
    sdk = installed / 'node_modules/@anthropic-ai/claude-agent-sdk'
    package = json.loads((sdk / 'package.json').read_text())
    assert package['name'] == '@anthropic-ai/claude-agent-sdk' and package['version']
    assert not shutil.which('python3', path=env['PATH'])

    fixture = root / 'fixture'
    run(root, env, binary, 'prepare', '--agent', 'claude', '--case', 'SDK-CASE-042',
        '--fixture-dir', fixture)
    run(root, env, binary, 'official', 'verify', '--fixture-dir', fixture, '--timeout', '60s')
    verification, = fixture.glob('packages/*/verify/*/verification.json')
    assert json.loads(verification.read_text())['status'] == 'passed'
    report = json.loads(verification.with_name('replay-report.json').read_text())
    assert report['officialSDK'] == {'package': package['name'], 'version': package['version']}
    print(f'Claude tarball: installed case 042 strictly replayed with {package["name"]}@{package["version"]}.')


if __name__ == '__main__':
    if len(sys.argv) != 3:
        raise SystemExit('usage: check-agent-sdk-testify-package.py NATIVE_ARCHIVE CLAUDE_TARBALL')
    archive, tarball = (Path(arg).resolve(strict=True) for arg in sys.argv[1:])
    try:
        with tempfile.TemporaryDirectory(prefix='agent-sdk-testify-installed-') as temporary:
            root = Path(temporary).resolve()
            binary = check_codex(archive, root)
            (root / 'npm').mkdir()
            check_claude(tarball, binary, root / 'npm')
    except subprocess.CalledProcessError as exc:
        raise SystemExit(f'{exc}\n{exc.stdout}{exc.stderr}') from exc
