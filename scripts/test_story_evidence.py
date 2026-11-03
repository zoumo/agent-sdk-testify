"""Offline checks of the simulation catalog and documentation gate."""
import importlib.util
from pathlib import Path
import unittest
import subprocess
import sys
import json
import shutil
import tempfile

SPEC = importlib.util.spec_from_file_location("story_evidence", Path(__file__).with_name("check-story-evidence.py"))
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)


class StoryEvidenceTests(unittest.TestCase):
    def test_requirements_are_independent_of_provider_routes(self):
        root = Path(__file__).resolve().parents[1]
        cases, rows = gate.recording_mapping(root)
        by_id = {case['id']: case for case in cases}
        self.assertEqual(["SDK-CASE-003", "SDK-CASE-025", "SDK-CASE-026"],
                         [case['id'] for case in cases if not case['required']])
        self.assertTrue(by_id['SDK-CASE-012']['required'])
        approval = next(row for row in rows if row.startswith('| SDK-CASE-012 |'))
        self.assertIn('| required (codex optional) |', approval)
        self.assertIn('| planned |', approval)
        for case_id in ('SDK-CASE-017', 'SDK-CASE-032'):
            row = next(row for row in rows if row.startswith('| ' + case_id + ' |'))
            self.assertIn('| required (claude optional) |', row)

    def test_current_simulation_catalog_passes_without_historical_corpus(self):
        result = subprocess.run([sys.executable, str(Path(__file__).with_name('check-story-evidence.py'))], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('8 stories, 36 cases', result.stdout)

    def test_public_gate_rejects_synthetic_route_and_unexplained_unavailability(self):
        source = Path(__file__).resolve().parents[1]
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / 'repository'
            shutil.copytree(source, root, ignore=shutil.ignore_patterns('.git', '.artifacts', '__pycache__',
                                                                       '*.egg-info', 'build', 'dist'))
            binding = root / 'data/scenarios/bindings/codex.json'
            original = json.loads(binding.read_text())
            for mode, reason, diagnostic in [
                ('offline-only', 'synthetic check', 'invalid simulation route'),
                ('planned', '', 'unavailable route needs a reason'),
            ]:
                with self.subTest(mode=mode):
                    changed = json.loads(json.dumps(original))
                    changed['cases']['SDK-CASE-001']['execution'] = {'mode': mode, 'reason': reason}
                    binding.write_text(json.dumps(changed))
                    result = subprocess.run([sys.executable, str(root / 'scripts/check-story-evidence.py')], capture_output=True, text=True)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(diagnostic, result.stderr)

    def test_case_mapping_rejects_unknown_owner_or_duplicate_identity(self):
        case = {'id': 'SDK-CASE-001', 'storyId': 'SDK-US-001'}
        self.assertEqual([], gate.case_mapping_errors(['SDK-US-001'], [case]))
        self.assertTrue(gate.case_mapping_errors(['SDK-US-002'], [case]))
        self.assertTrue(gate.case_mapping_errors(['SDK-US-001'], [case, case]))
        self.assertTrue(gate.case_mapping_errors(['SDK-US-001'], [dict(case, id='SDK-US-001.01')]))

    def test_inventory_reports_missing_duplicate_and_unexpected_ids(self):
        expected = {"SDK-US-001", "SDK-US-002"}
        self.assertEqual([], gate.inventory_errors("| SDK-US-001 | x |\n| SDK-US-002 | y |", "SDK-US", expected))
        errors = gate.inventory_errors("| SDK-US-001 | x |\n| SDK-US-001 | y |\n| SDK-US-020 | z |", "SDK-US", expected)
        self.assertEqual(["SDK-US: missing SDK-US-002", "SDK-US: unexpected SDK-US-020", "SDK-US: duplicate SDK-US-001"], errors)

    def test_relative_links_resolve_from_each_document(self):
        root = Path(__file__).resolve().parents[1]
        self.assertEqual([], gate.link_errors(root / "docs/development.md", "[ok](development.md) [web](https://example.org)"))
        errors = gate.link_errors(root / "docs/development.md", "[bad](missing-issue8-file.md)")
        self.assertEqual(1, len(errors))
        self.assertIn("missing-issue8-file.md", errors[0])

    def test_local_fragment_must_name_a_real_heading(self):
        root = Path(__file__).resolve().parents[1]
        path = root / "docs/development.md"
        self.assertEqual([], gate.link_errors(path, "[ok](development.md#环境与结果)"))
        errors = gate.link_errors(path, "[bad](development.md#missing-evidence-heading)")
        self.assertEqual(1, len(errors))
        self.assertIn("missing-evidence-heading", errors[0])


if __name__ == "__main__":
    unittest.main()
