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


def verify_recording(root):
    item = json.loads((root / 'manifest.json').read_text())
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
