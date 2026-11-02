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
    if not re.fullmatch(r'agent-sdk-testify [0-9][^\s]*', version) or result.stderr:
        raise IdentityError('unsupported Go agent-sdk-testify version response')
    actual = {'path': str(path), 'sha256': digest, 'version': version}
    if expected is not None and actual['version'] != expected.get('version'):
        raise IdentityError('prepared Go agent-sdk-testify version changed')
    return actual
