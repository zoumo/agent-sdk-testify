"""Test-only identity check for the explicitly supplied Go protocol executable."""
import hashlib
import os
from pathlib import Path
import re
import subprocess

from record_user_story.environment import read_regular


class IdentityError(ValueError):
    """A fixed public message that contains no paths or private configuration."""


def identity(value=None, expected=None):
    value = value or os.environ.get('AGENT_SDK_TESTIFY_BIN')
    if not value or not Path(value).is_absolute():
        raise IdentityError('set AGENT_SDK_TESTIFY_BIN to an absolute Go agent-sdk-testify executable')
    try:
        path = Path(value).resolve(strict=True)
        if not path.is_file() or not os.access(path, os.X_OK):
            raise IdentityError('Go agent-sdk-testify executable unavailable')
        digest = hashlib.sha256(read_regular(path)).hexdigest()
    except OSError as exc:
        raise IdentityError('Go agent-sdk-testify executable unavailable') from exc
    # Reject changed bytes before executing even the version probe.
    if expected is not None and digest != expected.get('sha256'):
        raise IdentityError('prepared Go agent-sdk-testify binary changed')
    try:
        result = subprocess.run([str(path), 'version'], capture_output=True, timeout=5,
                                env={'PATH': os.defpath}, check=True)
        version = result.stdout.decode().strip()
    except (OSError, subprocess.SubprocessError, UnicodeError) as exc:
        raise IdentityError('Go agent-sdk-testify version probe failed') from exc
    if not re.fullmatch(r'agent-sdk-testify (?:devel|v[0-9][^\s]*)', version) or result.stderr:
        raise IdentityError('unsupported Go agent-sdk-testify version response')
    actual = {'path': str(path), 'sha256': digest, 'version': version}
    if expected is not None and actual['version'] != expected.get('version'):
        raise IdentityError('prepared Go agent-sdk-testify version changed')
    return actual


# Synthetic test materials are private inputs to the same installed CLI used by
# consumers. This adapter never matches frames or supplies protocol responses.
_mock_runs = {}


def mock_command(provider, source, *args, binary=None):
    import json
    import tempfile
    import atexit
    import shutil
    binary = binary or identity()['path']
    root = Path(tempfile.mkdtemp(prefix='testify-public-mock-'))
    atexit.register(shutil.rmtree, root, ignore_errors=True)
    material = root / 'material'
    (material / 'raw').mkdir(parents=True)
    source = Path(source)
    raw = source.read_bytes() if source.exists() else b''
    (material / 'raw/raw.jsonl').write_bytes(raw)
    plan = {'provider': provider, 'caseId': 'synthetic-protocol', 'driverProfile': 'continuation',
            'project': str(root / 'project'), 'scenario': {'files': {}}}
    data = json.dumps(plan).encode()
    (material / 'plan.json').write_bytes(data)
    (material / 'manifest.json').write_text(json.dumps({'provenance': 'synthetic',
        'planSHA256': hashlib.sha256(data).hexdigest(), 'files': {'raw.jsonl': hashlib.sha256(raw).hexdigest()}}))
    fixture = root / 'fixture'
    prepare = [binary, 'prepare', '--recording', str(material), '--fixture-dir', str(fixture)]
    invocation, preparation, receipt = [], [], None
    it = iter(args)
    for arg in it:
        if arg == '--receipt': receipt = Path(next(it))
        elif arg in ('--max-frames', '--max-frame-bytes'):
            preparation += [arg, next(it)]
        elif arg == '--max-bytes': preparation += ['--protocol-bytes', next(it)]
        else: invocation.append(arg)
    prepare += preparation
    result = subprocess.run(prepare, capture_output=True, timeout=5)
    if result.returncode:
        # Return the actual failing public invocation for exit-code assertions.
        return prepare
    descriptor = json.loads((fixture / 'fixture.json').read_text())
    atexit.register(shutil.rmtree, descriptor['resourceRoot'], ignore_errors=True)
    command = [binary, 'agent', 'mock', '--fixture-dir', str(fixture), *invocation]
    _mock_runs[tuple(command)] = (fixture, receipt)
    return command


def run_mock(command, **kwargs):
    import json
    result = subprocess.run(command, **kwargs)
    fixture, receipt = _mock_runs.get(tuple(command), (None, None))
    if result.returncode == 0 and receipt is not None:
        index = json.loads((fixture / 'executions/index.json').read_text())
        execution = json.loads((fixture / 'executions' / index['executions'][-1]['id'] / 'execution.json').read_text())
        actual = execution['connections'][0]['receipt']
        # A test convenience copy of the actual public execution receipt.
        with receipt.open('xb') as target:
            target.write(Path(actual).read_bytes())
    return result



def prepare_fixture(root, plan, binary=None):
    import json
    import atexit
    import shutil
    binary = binary or identity()['path']
    source = root / 'consumer-plan.json'
    source.write_text(json.dumps({'version': 1, 'agentType': plan['provider'],
        'caseId': plan['caseId'], 'driverProfile': 'consumer', 'project': plan['project'],
        'scenario': {'files': plan.get('scenario', {}).get('files', {})}}))
    fixture = root / 'fixture'
    result = subprocess.run([binary, 'prepare', '--plan', str(source), '--fixture-dir', str(fixture)],
                            capture_output=True, timeout=5)
    if result.returncode:
        raise ValueError(result.stderr.decode())
    descriptor = json.loads((fixture / 'fixture.json').read_text())
    atexit.register(shutil.rmtree, descriptor['resourceRoot'], ignore_errors=True)
    return fixture


def current_capture(fixture):
    import json
    current = json.loads((fixture / 'current.json').read_text())
    index = json.loads((fixture / 'materials/index.json').read_text())
    return Path(next(item['path'] for item in index['materials'] if item['id'] == current['materialId']))
