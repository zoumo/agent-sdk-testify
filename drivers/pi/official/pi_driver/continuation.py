"""Run two successive prompts through one official Pi RpcClient."""
import json
from pathlib import Path
import subprocess
import sys

from record_user_story.adapters.common import proxy_command
from record_user_story.runtime_config import execution_workspace
from record_user_story.scenarios import Untriggered


SDK_PACKAGE = '@earendil-works/pi-coding-agent'
SDK_VERSION = '1.6.0'


def run(plan, report, flow):
    sdk = Path(sys.prefix) / 'node_modules' / SDK_PACKAGE
    manifest_path = sdk / 'package.json'
    if not manifest_path.is_file():
        raise RuntimeError(f'install {SDK_PACKAGE}@{SDK_VERSION} under the official driver environment')
    manifest = json.loads(manifest_path.read_text())
    if (manifest.get('name'), manifest.get('version')) != (SDK_PACKAGE, SDK_VERSION):
        raise ValueError(f'expected {SDK_PACKAGE}@{SDK_VERSION}')
    report['officialSDK'] = {'package': SDK_PACKAGE, 'version': SDK_VERSION}
    first, second = flow.inputs()
    completed = subprocess.run(
        ['node', str(Path(__file__).with_suffix('.mjs'))],
        check=True, text=True, stdout=subprocess.PIPE,
        input=json.dumps({
            'sdk': str(sdk / manifest['main']),
            'cwd': str(execution_workspace(plan)),
            'peer': proxy_command(plan),
            'prompts': [first, second],
        }),
    )
    observed = json.loads(completed.stdout)
    report['sessionId'] = observed['sessionId']
    for turn in observed['turns']:
        flow.submit()
        report['inputs'] += 1
        report['events'] += turn['events']
        report['receipts'].append(turn)
        flow.result()
        report['results'] += 1


def require_wire(frames, report):
    """Correlate both prompt results and the surrounding native state reads."""
    steps = []
    active = None
    for frame in frames:
        wire = frame['wire']
        kind = wire.get('type')
        if frame['direction'] == 'c2s' and kind in ('get_state', 'prompt'):
            if active is not None:
                raise Untriggered('Pi submitted another command before the current step settled')
            active = {'type': kind, 'id': wire.get('id')}
        elif frame['direction'] == 's2c' and kind == 'response' and active is not None:
            if (not active['id'] or wire.get('id') != active['id']
                    or wire.get('command') != active['type'] or wire.get('success') is not True):
                raise Untriggered('Pi response did not acknowledge the active command')
            data = wire.get('data', {})
            if active['type'] == 'get_state':
                steps.append(('get_state', data.get('sessionId')))
                active = None
            elif data.get('disposition') == 'started':
                active['started'] = True
        elif frame['direction'] == 's2c' and kind == 'agent_settled':
            if not active or not active.get('started') or wire.get('aborted') is not False:
                raise Untriggered('Pi has a terminal event without a successful prompt')
            steps.append(('prompt', 'agent_settled'))
            active = None

    session_id = report.get('sessionId')
    if not session_id or active is not None or steps != [
        ('get_state', session_id), ('prompt', 'agent_settled'),
        ('prompt', 'agent_settled'), ('get_state', session_id),
    ]:
        raise Untriggered('Pi wire evidence does not show two settled prompts in the same session')
