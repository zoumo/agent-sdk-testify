"""Configure repository-owned scenarios for SDK integration tests only."""
import os
from pathlib import Path
import sys
ROOT = Path(__file__).resolve().parents[3]
PACKAGE = ROOT / 'drivers/common'
sys.path.insert(0, str(PACKAGE))
sys.path.append(str(Path(__file__).resolve().parents[1]))
sys.path[:0] = [str(PACKAGE.parent / agent / 'official') for agent in ('codex', 'claude')]
os.environ['AGENT_MOCK_SCENARIOS'] = str(ROOT / 'data/scenarios')
BOOTSTRAP = Path(__file__).parent / 'recording_bootstrap.py'
