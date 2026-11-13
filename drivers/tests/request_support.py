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
    replay = bool(plan.get('_replay')) or (root / 'raw' / 'raw.jsonl').exists()
    runtime = {'schemaVersion': 1, 'root': str(root), 'workspace': plan.get('project', str(root)),
               'home': str(root / 'home'), 'config': str(root / 'config'), 'raw': str(root / 'raw'),
               'tmp': str(root / 'tmp'), 'output': str(root), 'replay': replay, 'peer': peer,
               'agentResults': plan.get('_agentResults', {})}
    if plan.get('_fixtureDir'):
        runtime['fixtureDir'] = plan['_fixtureDir']
    runtime_path = root / 'runtime.json'
    runtime_path.write_text(json.dumps(runtime))
    request = {'case': plan['caseId'], 'materials': str(materials), 'runtime': str(runtime_path)}
    path = root / 'request.json'
    path.write_text(json.dumps(request))
    registry = {plan['caseId']: plan['driverProfile']}
    with patch.dict(os.environ, {}, clear=True):
        assert main(['--request', str(path)], expected_provider=plan['provider'], case_registry=registry) == 0
    result = json.loads((root / 'result.json').read_text())
    assert (result['status'] == 'failed') == bool(expected_exit), result
    return result
