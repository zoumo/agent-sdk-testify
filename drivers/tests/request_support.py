"""Offline SDK substitutes exercise the installed single-request driver seam."""
import json
import os
from pathlib import Path
from unittest.mock import patch

from record_user_story.driver import main


def request_case(plan, expected_exit=0, peer=None):
    root = Path(plan['root'])
    materials = root / 'request-materials'
    materials.mkdir(exist_ok=True)
    (materials / 'case.json').write_text(json.dumps(plan))
    peer = peer or {'command': '/unused-synthetic-peer', 'args': []}
    request = {'case': plan['caseId'], 'materials': str(materials),
               'workspace': plan.get('project', str(root)), 'peer': peer, 'output': str(root)}
    path = root / 'request.json'
    path.write_text(json.dumps(request))
    registry = {plan['caseId']: plan['driverProfile']}
    with patch.dict(os.environ, {}, clear=True):
        assert main(['--request', str(path)], expected_provider=plan['provider'], case_registry=registry) == 0
    result = json.loads((root / 'result.json').read_text())
    assert (result['status'] == 'failed') == bool(expected_exit), result
    return result
