"""Read the shared scenario catalog. Run cases with agent-sdk-testify."""
import argparse
import json
import os
from pathlib import Path
import sys


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['catalog'])
    parser.add_argument('--provider', choices=['codex', 'claude'])
    parser.add_argument('--scenarios', type=Path)
    parser.add_argument('--stories')
    parser.add_argument('--cases')
    args = parser.parse_args(argv)
    try:
        if args.scenarios:
            os.environ['AGENT_MOCK_SCENARIOS'] = str(args.scenarios.resolve(strict=True))
        from .scenarios import case_recipes, digest, select_story_groups
        stories = select_story_groups(args.stories) if args.stories else None
        providers = [args.provider] if args.provider else ['codex', 'claude']
        definitions = [item for provider in providers for item in case_recipes(provider, stories, args.cases)]
        print(json.dumps([dict(item, scenarioSHA256=digest(item)) for item in definitions], indent=2))
        return 0
    except (OSError, ValueError) as exc:
        print('Catalog input is invalid: ' + str(exc), file=sys.stderr)
        return 1
