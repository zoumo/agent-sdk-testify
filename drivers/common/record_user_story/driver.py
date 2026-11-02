"""One native Python SDK case per request; Go owns environment and processes.

Run directly with an installed SDK environment:
    python /absolute/record_user_story/driver.py --request request.json
"""
import argparse
import json
from pathlib import Path
import sys

if __package__ in (None, ''):
    sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from record_user_story.native_cases import prepare_case


def main(argv=None, expected_provider=None, case_registry=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--request', required=True, type=Path)
    args = parser.parse_args(argv)
    request = json.loads(args.request.read_text())
    output = Path(request['output'])
    result = {'status': 'failed', 'message': ''}
    try:
        plan = json.loads((Path(request['materials']) / 'case.json').read_text())
        if expected_provider and plan.get('provider') != expected_provider:
            raise ValueError('request provider differs from selected Agent driver')
        if plan['caseId'] != request['case']:
            raise ValueError('requested case differs from supplied materials')
        plan['_peer'] = request['peer']
        plan['_fixtureProject'] = request['workspace']
        plan['_nativeCase'] = True
        prepare_case(plan, case_registry or {})
        # Read-only evidence distinguishes saved inputs from a fresh capture;
        # process commands and configuration come exclusively from request.peer.
        plan['_replay'] = (output / 'raw/raw.jsonl').exists()
        from record_user_story.drivers import run
        run(plan)
        report_path = output / ('replay-report.json' if plan['_replay'] else 'report.json')
        report = json.loads(report_path.read_text())
        status = report['status']
        result = {'status': 'unsupported' if status in ('planned', 'unsupported') else status,
                  'message': '; '.join(report.get('gaps', [])),
                  'sdk': report.get('officialSDK')}
        diagnostics = output / ('replay-diagnostics.private.json' if plan['_replay'] else 'diagnostics.private.json')
        if status == 'failed' and diagnostics.exists():
            result['message'] = json.loads(diagnostics.read_text()).get('error', result['message'])
    except NotImplementedError as exc:
        result = {'status': 'unsupported', 'message': str(exc)}
    except Exception as exc:
        result = {'status': 'failed', 'message': f'{type(exc).__name__}: {exc}'}
    temporary = output / 'result.json.tmp'
    temporary.write_text(json.dumps(result, indent=2) + '\n')
    temporary.replace(output / 'result.json')
    return 0


if __name__ == '__main__':
    sys.exit(main())
