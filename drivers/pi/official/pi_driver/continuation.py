"""Run two successive prompts through one official Pi RpcClient."""
import json
from pathlib import Path
import subprocess
import sys

from record_user_story.adapters.common import proxy_command
from record_user_story.runtime_config import execution_workspace


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
