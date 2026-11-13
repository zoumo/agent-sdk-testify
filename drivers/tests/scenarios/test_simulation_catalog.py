"""Simulation stories expose real usage flows through the public catalog CLI."""
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import sys
import unittest

ROOT = Path(__file__).resolve().parents[3]
BOOTSTRAP = ROOT / 'drivers/common/record_user_story/bootstrap.py'
CATALOG = ROOT / 'data/scenarios'


class SimulationCatalogTests(unittest.TestCase):
    def catalog(self, *args, directory=CATALOG):
        return subprocess.run([sys.executable, str(BOOTSTRAP), 'catalog', '--scenarios', str(directory), *args],
                              cwd=ROOT, text=True, capture_output=True)

    def test_catalog_exposes_eight_real_stories_without_technical_routes(self):
        result = self.catalog('--provider', 'codex')
        self.assertEqual(result.returncode, 0, result.stderr)
        cases = json.loads(result.stdout)
        self.assertEqual(sorted({case['storyId'] for case in cases}), [f'SDK-US-{n:03}' for n in range(1, 9)])
        self.assertTrue(all(case['mode'] in ('live-selected-path', 'planned', 'unsupported') for case in cases))
        self.assertTrue(all('offlineTest' not in case for case in cases))

    def test_unavailable_flow_requires_an_explanation(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / 'scenarios'
            shutil.copytree(CATALOG, directory)
            path = directory / 'bindings/codex.json'
            bindings = json.loads(path.read_text())
            bindings['cases']['SDK-CASE-033']['execution'] = {'mode': 'planned', 'reason': ''}
            path.write_text(json.dumps(bindings))
            result = self.catalog('--provider', 'codex', '--cases', 'SDK-CASE-033', directory=directory)
            self.assertNotEqual(result.returncode, 0)

    def test_provider_routes_keep_real_unavailable_goals_visible(self):
        codex = json.loads(self.catalog('--provider', 'codex').stdout)
        claude = json.loads(self.catalog('--provider', 'claude').stdout)
        by_id = lambda cases: {case['caseId']: case for case in cases}
        self.assertEqual(len(codex), 38)
        self.assertEqual(len(claude), 38)
        c, a = by_id(codex), by_id(claude)
        self.assertEqual(c['SDK-CASE-003']['mode'], 'unsupported')
        self.assertEqual(a['SDK-CASE-003']['mode'], 'unsupported')
        self.assertIn('document', a['SDK-CASE-003']['reason'])
        self.assertEqual(a['SDK-CASE-017']['mode'], 'unsupported')
        self.assertEqual(a['SDK-CASE-026']['mode'], 'unsupported')
        self.assertIn('数值预算', a['SDK-CASE-026']['reason'])
        self.assertEqual(c['SDK-CASE-033']['mode'], 'live-selected-path')
        self.assertEqual(a['SDK-CASE-033']['driverProfile'], 'history')
        self.assertEqual(c['SDK-CASE-031']['driverProfile'], 'session-close')
        for cases in (codex, claude):
            for case in cases:
                if case['mode'] != 'live-selected-path':
                    self.assertTrue(case['reason'].strip(), case['caseId'])
                    self.assertEqual(case['prompts'], [])
                    self.assertEqual(case['steps'], [])
        self.assertEqual(c['SDK-CASE-035']['mode'], 'live-selected-path')
        self.assertEqual(a['SDK-CASE-035']['driverProfile'], 'existing-children')
        self.assertEqual(a['SDK-CASE-035']['prompts'], [])
        self.assertEqual(a['SDK-CASE-035']['steps'], [
            {'op': 'session.open'}, {'op': 'history.discover'}, {'op': 'session.close'}])

    def test_story_and_case_selection_intersect_without_fallback(self):
        result = self.catalog('--provider', 'claude', '--stories', 'SDK-US-008', '--cases', 'SDK-CASE-033')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([case['caseId'] for case in json.loads(result.stdout)], ['SDK-CASE-033'])
        for args in (('--stories', 'SDK-US-009'), ('--cases', 'SDK-CASE-099'),
                     ('--stories', 'SDK-US-001', '--cases', 'SDK-CASE-033')):
            with self.subTest(args=args):
                self.assertNotEqual(self.catalog('--provider', 'codex', *args).returncode, 0)

    def test_technical_and_fixture_only_routes_are_rejected(self):
        for field, value in (('implementation', 'fixture-only'), ('mode', 'offline-only')):
            with self.subTest(field=field), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary) / 'scenarios'
                shutil.copytree(CATALOG, directory)
                path = directory / 'SDK-US-001.json'
                story = json.loads(path.read_text())
                target = story['cases'][0] if field == 'implementation' else story['cases'][0]['execution']
                target[field] = value
                path.write_text(json.dumps(story))
                self.assertNotEqual(self.catalog('--provider', 'codex', directory=directory).returncode, 0)

    def test_standalone_catalog_accepts_application_defined_identities(self):
        result = self.catalog('--provider', 'claude', directory=ROOT / 'drivers/common/record_user_story/examples')
        self.assertEqual(result.returncode, 0, result.stderr)
        cases = json.loads(result.stdout)
        self.assertEqual([(case['storyId'], case['caseId']) for case in cases], [('conversation', 'two-inputs')])
        self.assertEqual(cases[0]['driverProfile'], 'continuation')
