#!/usr/bin/env python3
"""Check the installed native driver contract outside the source checkout."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import venv


def check(wheel):
    with tempfile.TemporaryDirectory(prefix='agent-sdk-testify-installed-') as temporary:
        root = Path(temporary).resolve()
        venv.EnvBuilder(with_pip=True, symlinks=True).create(root / 'env')
        python = root / 'env/bin/python'
        environment = {'PATH': str(python.parent) + os.pathsep + os.defpath, 'HOME': str(root),
                       'PIP_CONFIG_FILE': os.devnull}
        def run(*args):
            return subprocess.run([str(x) for x in args], cwd=root, env=environment,
                                  capture_output=True, check=True, timeout=60)
        run(python, '-I', '-m', 'pip', 'install', '--no-index', '--no-deps', wheel)
        run(python, '-I', '-c', "from importlib.metadata import distribution; "
            "d=distribution('agent-sdk-testify-driver'); "
            "assert {e.name for e in d.entry_points} == {'agent-sdk-testify-codex', 'agent-sdk-testify-claude'}; "
            "assert all('extra ==' in r for r in d.requires or [])")
        materials, output = root / 'materials', root / 'output'
        materials.mkdir(); output.mkdir()
        for provider in ('codex', 'claude'):
            (materials / 'case.json').write_text(json.dumps({'caseId': 'not-implemented', 'provider': provider, 'scenario': {}}))
            runtime = {'schemaVersion': 1, 'root': str(root), 'workspace': str(root),
                       'home': str(root / 'home'), 'config': str(root / 'config'),
                       'raw': str(root / 'raw'), 'tmp': str(root / 'tmp'),
                       'output': str(output), 'replay': False,
                       'peer': {'command': '/must-not-launch', 'args': []}}
            (root / 'runtime.json').write_text(json.dumps(runtime))
            request = {'case': 'not-implemented', 'materials': str(materials),
                       'runtime': str(root / 'runtime.json')}
            (root / 'request.json').write_text(json.dumps(request))
            run(python.parent / ('agent-sdk-testify-' + provider), '--request', root / 'request.json')
            assert json.loads((output / 'result.json').read_text())['status'] == 'unsupported'
            print(f'{provider}: installed driver returned unsupported; sentinel Agent command was not launched.')
            (output / 'result.json').unlink()
        help_result = run(python, '-I', '-m', 'record_user_story.bootstrap', '--help')
        assert '{catalog}' in help_result.stdout.decode()
        run(python, '-I', '-c', "from importlib.util import find_spec; "
            "assert all(find_spec('record_user_story.' + name) is None "
            "for name in ('proxy', 'remote_proxy', 'peer'))")
        assert not (python.parent / 'record-user-story').exists()
    print('Installed agent-sdk-testify driver: request/result contract passed outside checkout; no SDK or Agent launched.')


if __name__ == '__main__':
    if len(sys.argv) != 2:
        raise SystemExit('usage: check-agent-sdk-testify-package.py DRIVER_WHEEL')
    check(Path(sys.argv[1]).resolve(strict=True))
