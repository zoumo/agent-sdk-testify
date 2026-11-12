"""Private material I/O and verification; the Go runner prepares environments.

Saved recording manifests remain readable for source and seed integrity checks.
No native executable, authentication or configuration is discovered here.
"""
import hashlib
import json
import os
from pathlib import Path
import stat
import tempfile

def recording_temp_root():
    return Path(tempfile.gettempdir()).resolve(strict=True)


def stdin_eof_action(plan):
    """Validate the frozen close policy without interpreting native frames."""
    action = plan.get('stdinEOFAction', 'close')
    if action not in ('close', 'interrupt'):
        raise ValueError('invalid frozen stdin EOF action')
    if action == 'interrupt' and (plan.get('provider') != 'claude'
                                  or plan.get('driverProfile') != 'interrupt'):
        raise ValueError('SIGINT close is restricted to the Claude interrupt profile')
    return action


def private_json(path, value):
    with path.open('x', encoding='utf-8') as f:
        os.chmod(path, 0o600)
        json.dump(value, f, indent=2)
        f.write('\n')
        f.flush()
        os.fsync(f.fileno())


def read_regular(path, limit=None):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or (limit is not None and info.st_size > limit):
            raise ValueError('regular file required; requested read limit must not be exceeded')
        with os.fdopen(fd, 'rb', closefd=False) as source:
            return source.read() if limit is None else source.read(limit + 1)
    finally:
        os.close(fd)


def _current_public_result(plan):
    """Bind schema 3 package references to the explicit current fixture root."""
    data = read_regular(Path(plan['_agentResults']['main']), 1024 * 1024)
    if not data.endswith(b'\n'):
        raise ValueError('public command notification is incomplete')
    notices = [json.loads(line) for line in data.splitlines()]
    if len(notices) != 2:
        raise ValueError('public command did not settle')
    started, finished = notices
    if (started.get('schemaVersion') != 3 or finished.get('schemaVersion') != 3
            or started.get('status') != 'running' or finished.get('status') != 'passed'
            or started.get('connectionId') != 'main'
            or any(started.get(key) != finished.get(key) for key in (
                'fixtureId', 'materialId', 'executionId', 'connectionId', 'processId',
                'command', 'execution', 'executionSHA256', 'result'))):
        raise ValueError('public command results disagree')
    fixture = plan.get('_fixtureDir')
    if not isinstance(fixture, str) or not Path(fixture).is_absolute():
        raise ValueError('schema 3 public command requires runtime fixtureDir')
    fixture = Path(fixture).resolve(strict=True)
    control = json.loads(read_regular(fixture / 'fixture.json', 1024 * 1024))
    if control.get('schemaVersion') != 7 or control.get('fixtureId') != finished.get('fixtureId'):
        raise ValueError('public command fixture identity differs')
    for key in ('materialId', 'executionId'):
        value = finished.get(key)
        if (not isinstance(value, str) or not value or value in ('.', '..')
                or '/' in value or '\\' in value or '\x00' in value):
            raise ValueError('public command identity is invalid')
    package = fixture / 'packages' / finished['materialId']
    if not package.resolve().is_relative_to(fixture):
        raise ValueError('public command package escapes fixture')
    execution = 'executions/' + finished['executionId']

    def reference(name, expected):
        if name != expected:
            raise ValueError('public command package reference differs')
        path = package / name
        if not path.resolve().is_relative_to(package.resolve()):
            raise ValueError('public command package reference escapes')
        return path

    declaration_path = reference(finished['execution'], execution + '/execution.json')
    declaration_bytes = read_regular(declaration_path, 1024 * 1024)
    if hashlib.sha256(declaration_bytes).hexdigest() != finished.get('executionSHA256'):
        raise ValueError('public command execution hash differs')
    declaration = json.loads(declaration_bytes)
    if (declaration.get('schemaVersion') != 7
            or declaration.get('fixtureId') != finished['fixtureId']
            or declaration.get('executionId') != finished['executionId']
            or declaration.get('artifact', {}).get('id') != finished['materialId']
            or declaration.get('command') != finished.get('command')
            or declaration.get('command') not in ('record', 'mock')):
        raise ValueError('public command execution identity differs')
    result_path = reference(finished['result'], execution + '/receipts/main.result.json')
    if json.loads(read_regular(result_path, 1024 * 1024)) != finished:
        raise ValueError('public command notification differs from durable result')
    receipt_name = execution + '/receipts/main.json'
    if finished['command'] == 'record':
        receipt_name = execution + '/receipts/main.transport.json'
    receipt = reference(finished['receipt'], receipt_name)
    core = package / 'material'
    captured = core if finished['command'] == 'mock' or finished.get('material') else package / execution / 'capture'
    if not captured.resolve().is_relative_to(package.resolve()):
        raise ValueError('public command capture reference escapes')
    return captured, receipt


def capture_root(plan):
    """Resolve this SDK launch's ordinary result; never select a recent run."""
    results = plan.get('_agentResults')
    if not results:
        return Path(plan['root'])
    notices = [json.loads(line) for line in read_regular(Path(results['main']), 1024 * 1024).splitlines()]
    if notices and 'schemaVersion' in notices[0]:
        return _current_public_result(plan)[0]
    if len(notices) != 2:
        raise ValueError('record command did not settle')
    started, finished = notices
    if (started.get('status') != 'running' or finished.get('status') != 'passed'
            or started.get('connectionId') != 'main'
            or any(started.get(key) != finished.get(key)
                   for key in ('executionId', 'fixtureId', 'connectionId', 'execution', 'processId'))):
        raise ValueError('record command results disagree')
    execution = json.loads(read_regular(Path(finished['execution']), 1024 * 1024))
    if (execution.get('executionId') != finished['executionId']
            or execution.get('fixtureId') != finished['fixtureId'] or execution.get('command') not in ('record', 'mock')):
        raise ValueError('record execution differs from result')
    if execution['command'] == 'mock':
        return Path(execution['artifact']['path'])
    return Path(execution['artifactRoot'])


def recording_path(root, reference):
    if (not isinstance(reference, str) or not reference or "\\" in reference
            or "\x00" in reference or str(Path(reference)) != reference
            or Path(reference).is_absolute() or any(x in ('.', '..') for x in reference.split('/'))):
        raise ValueError('invalid frozen seed reference')
    current = root
    for part in reference.split('/'):
        current = current / part
        if current.is_symlink():
            raise ValueError('frozen seed reference traverses symlink')
    return current


def verify_recording(root):
    item = json.loads(read_regular(root / 'manifest.json'))
    if item.get('materialFormatVersion') == 3:
        files = item.get('files')
        if not isinstance(files, dict) or not files:
            raise ValueError('material inventory required')
        actual = {}
        for path in root.rglob('*'):
            if path.is_symlink():
                raise ValueError('material inventory traverses symlink')
            if path.is_dir():
                continue
            if not path.is_file():
                raise ValueError('material inventory has an unsupported entry')
            name = path.relative_to(root).as_posix()
            if name not in ('manifest.json', 'material.json'):
                actual[name] = hashlib.sha256(read_regular(recording_path(root, name), None)).hexdigest()
        if actual != files:
            raise ValueError('material inventory changed')
        if hashlib.sha256(read_regular(root / 'plan.json')).hexdigest() != item.get('planSHA256'):
            raise ValueError('recording plan changed')
        return
    if item.get('remotePhaseFiles'):
        from codex_driver.remote import verify_phase_files
        verify_phase_files(root, item['remotePhaseFiles'])
    if item.get('planSHA256') and hashlib.sha256(read_regular(root / 'plan.json')).hexdigest() != item['planSHA256']:
        raise ValueError('recording plan changed')
    for name, digest in item['files'].items():
        path = root / 'raw' / name
        if Path(name).name != name or path.is_symlink() or hashlib.sha256(read_regular(path, None)).hexdigest() != digest:
            raise ValueError('recording changed')
    for name, digest in item.get('caseFiles', {}).items():
        if name not in ['project/child-fixture.txt', 'config/agents/fixture_reader.toml'] or hashlib.sha256(read_regular(root / name)).hexdigest() != digest:
            raise ValueError('synthetic child case file changed')
    for name, digest in item.get('driverFiles', {}).items():
        path = root / name
        if not name.startswith('driver/') or '..' in Path(name).parts or path.is_symlink() or hashlib.sha256(read_regular(path)).hexdigest() != digest:
            raise ValueError('recording driver snapshot changed')
    for name, digest in item.get('savedFiles', {}).items():
        path = root / name
        if not name.startswith(('config/projects/', 'config/sessions/')) or '..' in Path(name).parts or path.is_symlink() or hashlib.sha256(read_regular(path)).hexdigest() != digest:
            raise ValueError('saved recording source changed')


def sdk_identity(provider):
    from importlib.metadata import version
    from .policy import SDK_VERSIONS
    name, _ = SDK_VERSIONS[provider]
    actual = version(name)
    return {'package': name, 'version': actual}


def replay_receipt(plan):
    """Read the launched public mock's receipt, separate from immutable raw input."""
    results = plan.get('_agentResults')
    if not results:
        return json.loads(read_regular(Path(plan['root']) / 'replay-peer.json'))
    notices = [json.loads(line) for line in read_regular(Path(results['main']), 1024 * 1024).splitlines()]
    if notices and 'schemaVersion' in notices[0]:
        if notices[-1].get('command') != 'mock':
            raise ValueError('mock command did not settle')
        return json.loads(read_regular(_current_public_result(plan)[1]))
    if len(notices) != 2 or notices[-1].get('command') != 'mock' or notices[-1].get('status') != 'passed':
        raise ValueError('mock command did not settle')
    return json.loads(read_regular(Path(notices[-1]['receipt'])))
