"""Standalone Claude elicitation uses the official TypeScript SDK callback."""
import json
from pathlib import Path
import subprocess
import sys

from record_user_story.adapters.common import proxy_command
from record_user_story.runtime_config import execution_workspace
from record_user_story.scenarios import Untriggered


SDK_PACKAGE = '@anthropic-ai/claude-agent-sdk'
SDK_VERSION = '0.3.293'


def run(plan, report, flow):
    package = Path(sys.prefix) / 'node_modules' / SDK_PACKAGE
    manifest = package / 'package.json'
    if not manifest.is_file():
        raise RuntimeError(f'install {SDK_PACKAGE}@{SDK_VERSION} under the official driver environment')
    identity = json.loads(manifest.read_text())
    if (identity.get('name'), identity.get('version')) != (SDK_PACKAGE, SDK_VERSION):
        raise RuntimeError(f'expected {SDK_PACKAGE}@{SDK_VERSION}')
    report['officialSDK'] = {'package': SDK_PACKAGE, 'version': SDK_VERSION}
    prompt, = flow.inputs()
    flow.submit()
    command, *args = proxy_command(plan)
    completed = subprocess.run(['node', str(Path(__file__).with_suffix('.mjs'))], check=True,
                               input=json.dumps({'command': command, 'args': args, 'prompt': prompt,
                                                 'cwd': str(execution_workspace(plan)), 'sdk': str(package / 'sdk.mjs')}),
                               text=True, stdout=subprocess.PIPE)
    outcome = json.loads(completed.stdout)
    result = outcome.get('result') or {}
    if len(outcome['receipts']) != 6 or result.get('subtype') != 'success' or result.get('is_error') is not False:
        raise Untriggered('standalone Claude elicitation did not complete all six decisions and the root result')
    report.update(inputs=1, results=1, events=outcome['events'], requests=6)
    report['receipts'].extend({'elicitation': receipt} for receipt in outcome['receipts'])
    report['receipts'].append({'result': result})
    flow.request('standalone-elicitation')
    flow.result()
