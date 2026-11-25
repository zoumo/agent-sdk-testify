#!/usr/bin/env python3
"""Install and check the official driver artifacts outside the source checkout."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import venv


def run(root, environment, *args):
    return subprocess.run([str(x) for x in args], cwd=root, env=environment,
                          capture_output=True, check=True, timeout=90, text=True)


def check_python_drivers(wheel, root):
    venv.EnvBuilder(with_pip=True, symlinks=True).create(root / 'env')
    python = root / 'env/bin/python'
    environment = {'PATH': str(python.parent) + os.pathsep + os.defpath, 'HOME': str(root),
                   'PIP_CONFIG_FILE': os.devnull}
    run(root, environment, python, '-I', '-m', 'pip', 'install', '--no-index', '--no-deps', wheel)
    run(root, environment, python, '-I', '-c', """
from importlib.metadata import distribution
from importlib.util import find_spec

driver = distribution('agent-sdk-testify-driver')
assert {entry.name for entry in driver.entry_points} == {'agent-sdk-testify-codex', 'agent-sdk-testify-pi'}
assert driver.metadata.get_all('Provides-Extra') == ['codex']
assert all('extra ==' in requirement and 'claude' not in requirement
           for requirement in driver.requires or [])
assert all(find_spec(name) is None for name in ('claude_driver', 'claude_agent_sdk'))
assert all(find_spec('record_user_story.' + name) is None
           for name in ('proxy', 'remote_proxy', 'peer'))
""")
    materials, output = root / 'materials', root / 'output'
    materials.mkdir()
    output.mkdir()
    runtime = root / 'runtime.json'
    runtime.write_text(json.dumps({
        'schemaVersion': 1, 'root': str(root), 'workspace': str(root),
        'home': str(root / 'home'), 'config': str(root / 'config'),
        'raw': str(root / 'raw'), 'tmp': str(root / 'tmp'),
        'output': str(output), 'replay': False,
        'peer': {'command': '/must-not-launch', 'args': []},
    }))
    request = root / 'request.json'
    request.write_text(json.dumps({
        'case': 'not-implemented', 'materials': str(materials), 'runtime': str(runtime),
    }))
    for provider in ('codex', 'pi'):
        (materials / 'case.json').write_text(json.dumps({
            'caseId': 'not-implemented', 'provider': provider, 'scenario': {},
        }))
        run(root, environment, python.parent / f'agent-sdk-testify-{provider}', '--request', request)
        result = output / 'result.json'
        assert json.loads(result.read_text())['status'] == 'unsupported'
        result.unlink()
    help_result = run(root, environment, python, '-I', '-m', 'record_user_story.bootstrap', '--help')
    assert '{catalog}' in help_result.stdout
    for command in ('agent-sdk-testify-claude', 'record-user-story'):
        assert not (python.parent / command).exists()
    print('Codex and Pi wheel: installed request/result and catalog contracts passed.')


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
    if len(sys.argv) != 4:
        raise SystemExit('usage: check-agent-sdk-testify-package.py CODEX_WHEEL CLAUDE_TARBALL TOOL_BINARY')
    wheel, tarball, binary = (Path(arg).resolve(strict=True) for arg in sys.argv[1:])
    try:
        with tempfile.TemporaryDirectory(prefix='agent-sdk-testify-installed-') as temporary:
            root = Path(temporary).resolve()
            for name in ('wheel', 'npm'):
                (root / name).mkdir()
            check_python_drivers(wheel, root / 'wheel')
            check_claude(tarball, binary, root / 'npm')
    except subprocess.CalledProcessError as exc:
        raise SystemExit(f'{exc}\n{exc.stdout}{exc.stderr}') from exc
