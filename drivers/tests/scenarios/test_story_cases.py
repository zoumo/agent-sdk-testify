"""Story composition and case identity are independent of driver selection."""
from copy import deepcopy
import json
from pathlib import Path
import shutil
import tempfile
import unittest

from support import BOOTSTRAP
from record_user_story.scenarios import case_recipes, recipe, scenario_directory, story_documents, summarize_story
from record_user_story.policy import driver_profile


class StoryCaseTests(unittest.TestCase):
    def test_provider_requirement_override_works_with_packaged_schema(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp) / 'scenarios'
            shutil.copytree(scenario_directory(), directory)
            (directory / 'bindings.schema.json').unlink()
            selected = recipe('claude', 'SDK-US-005', directory, case_id='SDK-CASE-017')
            self.assertFalse(selected['required'])
            binding_path = directory / 'bindings/claude.json'
            binding = json.loads(binding_path.read_text())
            binding['cases']['SDK-CASE-017']['required'] = 'optional'
            binding_path.write_text(json.dumps(binding))
            with self.assertRaises(ValueError):
                recipe('claude', 'SDK-US-005', directory, case_id='SDK-CASE-017')

    def test_provider_optional_requirements_do_not_waive_codex_or_display(self):
        for provider in ('claude', 'codex'):
            cases = {case['caseId']: case for case in case_recipes(provider)}
            self.assertEqual(sum(case['required'] for case in cases.values()), 31 if provider == 'claude' else 33)
            for case_id in ('SDK-CASE-017', 'SDK-CASE-032'):
                self.assertEqual(cases[case_id]['required'], provider == 'codex')
            self.assertTrue(cases['SDK-CASE-028']['required'])
            self.assertIn('不作为门禁条件', cases['SDK-CASE-028']['scope'])

    def test_case_identity_survives_story_text_and_case_order_changes(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp) / 'scenarios'
            shutil.copytree(scenario_directory(), directory)
            before = recipe('codex', 'SDK-US-001', directory, case_id='SDK-CASE-001')
            path = directory / 'SDK-US-001.json'
            document = json.loads(path.read_text())
            document['goal'] = 'Reworded complete user story'
            document['cases'].reverse()
            path.write_text(json.dumps(document))
            after = recipe('codex', 'SDK-US-001', directory, case_id='SDK-CASE-001')
            self.assertEqual((before['storyId'], before['caseId']), (after['storyId'], after['caseId']))
            self.assertEqual(before['prompts'], after['prompts'])
            self.assertEqual(before['driverProfile'], after['driverProfile'])


    def test_unavailable_routes_cannot_be_promoted_by_forged_pass_reports(self):
        for provider in ('codex', 'claude'):
            checked = 0
            for story in story_documents():
                cases = case_recipes(provider, stories=story['id'])
                unavailable = [c['caseId'] for c in cases if c['required'] and c['mode'] != 'live-selected-path']
                if not unavailable:
                    continue
                results = {case['caseId']: {'status': 'passed', 'evidenceLayer': 'protocol-replay'} for case in cases}
                summary = summarize_story(provider, story['id'], results, 'protocol-replay')
                self.assertEqual(summary['status'], 'partial')
                for case_id in unavailable:
                    self.assertIn(case_id, summary['missingCases'])
                    checked += 1
            self.assertEqual(checked, 0 if provider == 'claude' else 1)

    def test_catalog_retains_stable_stories_and_all_small_validation_targets(self):
        stories = story_documents()
        self.assertEqual([item['id'] for item in stories], [f'SDK-US-{i:03}' for i in range(1, 9)])
        cases = [case for story in stories for case in story['cases']]
        self.assertEqual(len(cases), 36)
        self.assertEqual(sum(case['required'] for case in cases), 33)
        self.assertEqual([case['id'] for case in cases if not case['required']],
                         ['SDK-CASE-003', 'SDK-CASE-025', 'SDK-CASE-026'])
        self.assertEqual(len({case['id'] for case in cases}), len(cases))
        self.assertTrue(all(len(story['cases']) > 1 for story in stories))
        for provider in ('codex', 'claude'):
            selected = case_recipes(provider, stories='SDK-US-001')
            self.assertEqual([case['caseId'] for case in selected], ['SDK-CASE-001', 'SDK-CASE-002', 'SDK-CASE-003', 'SDK-CASE-004', 'SDK-CASE-005'])
            self.assertEqual(selected[1]['mode'], 'live-selected-path')
            self.assertNotIn('offlineTest', selected[1])
            with self.assertRaises(ValueError):
                case_recipes(provider, stories='SDK-US-002', cases='SDK-CASE-001')

    def test_small_case_success_cannot_pass_whole_story_or_mix_evidence_layers(self):
        results = {'SDK-CASE-001': {'status': 'passed', 'evidenceLayer': 'protocol-replay'}}
        summary = summarize_story('claude', 'SDK-US-001', results, 'protocol-replay')
        self.assertEqual(summary['status'], 'partial')
        self.assertEqual(summary['missingCases'], ['SDK-CASE-002', 'SDK-CASE-004', 'SDK-CASE-005'])
        results.update({key: {'status': 'passed', 'evidenceLayer': 'go-sdk'} for key in summary['missingCases']})
        self.assertEqual(summarize_story('claude', 'SDK-US-001', results, 'protocol-replay')['status'], 'partial')
        for result in results.values():
            result['evidenceLayer'] = 'go-sdk'
        self.assertEqual(summarize_story('claude', 'SDK-US-001', results, 'go-sdk')['status'], 'passed')
        results['SDK-CASE-002']['status'] = 'failed'
        self.assertEqual(summarize_story('claude', 'SDK-US-001', results, 'go-sdk')['status'], 'failed')

    def test_optional_thinking_mode_does_not_waive_required_effort(self):
        for provider in ('codex', 'claude'):
            cases = {case['caseId']: case for case in case_recipes(provider, stories='SDK-US-006')}
            self.assertFalse(cases['SDK-CASE-025']['required'])
            self.assertTrue(cases['SDK-CASE-027']['required'])
            if provider == 'codex':
                self.assertEqual(cases['SDK-CASE-025']['mode'], 'unsupported')
            results = {key: {'status': 'passed', 'evidenceLayer': 'go-sdk'}
                       for key, case in cases.items() if case['required']}
            self.assertEqual(summarize_story(provider, 'SDK-US-006', results, 'go-sdk')['status'], 'passed')
            results['SDK-CASE-025'] = {'status': 'unsupported', 'evidenceLayer': 'go-sdk'}
            self.assertEqual(summarize_story(provider, 'SDK-US-006', results, 'go-sdk')['status'], 'passed')
            del results['SDK-CASE-027']
            summary = summarize_story(provider, 'SDK-US-006', results, 'go-sdk')
            self.assertEqual(summary['status'], 'partial')
            self.assertIn('SDK-CASE-027', summary['missingCases'])

    def test_optional_failures_do_not_waive_or_block_required_cases(self):
        for provider in ('codex', 'claude'):
            cases = case_recipes(provider, stories='SDK-US-001')
            results = {case['caseId']: {'status': 'passed', 'evidenceLayer': 'go-sdk'}
                       for case in cases if case['required']}
            self.assertEqual(summarize_story(provider, 'SDK-US-001', results, 'go-sdk')['status'], 'passed')
            results['SDK-CASE-003'] = {'status': 'failed', 'evidenceLayer': 'go-sdk'}
            self.assertEqual(summarize_story(provider, 'SDK-US-001', results, 'go-sdk')['status'], 'passed')
        cases = case_recipes('codex', stories='SDK-US-003')
        results = {case['caseId']: {'status': 'passed', 'evidenceLayer': 'go-sdk'} for case in cases}
        summary = summarize_story('codex', 'SDK-US-003', results, 'go-sdk')
        self.assertEqual(summary['status'], 'partial')
        self.assertIn('SDK-CASE-012', summary['missingCases'])

    def test_multiple_executable_cases_in_one_story_do_not_select_a_default(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory = Path(tmp) / 'scenarios'
            shutil.copytree(scenario_directory(), directory)
            path = directory / 'SDK-US-001.json'
            document = json.loads(path.read_text())
            additional = deepcopy(document['cases'][0])
            additional['id'] = 'SDK-CASE-200'
            additional['inputs']['input-1'] = 'Different case input.'
            document['cases'].append(additional)
            path.write_text(json.dumps(document))
            binding = directory / 'bindings/codex.json'
            data = json.loads(binding.read_text())
            data['cases']['SDK-CASE-200'] = deepcopy(data['cases']['SDK-CASE-001'])
            binding.write_text(json.dumps(data))
            with self.assertRaisesRegex(ValueError, 'explicit case'):
                recipe('codex', 'SDK-US-001', directory)
            selected = case_recipes('codex', stories='SDK-US-001', cases='SDK-CASE-001,SDK-CASE-200', directory=directory)
            self.assertEqual(len(selected), 2)
            self.assertNotEqual(selected[0]['prompts'], selected[1]['prompts'])

    def test_driver_profile_does_not_depend_on_story_or_case_numbers(self):
        self.assertEqual(driver_profile({'version': 4, 'story': 'SDK-US-999', 'caseId': 'SDK-CASE-555',
                                       'driverProfile': 'continuation'}), 'continuation')
        with self.assertRaisesRegex(ValueError, 'explicit driver profile'):
            driver_profile({'version': 4, 'story': 'SDK-US-001', 'caseId': 'SDK-CASE-001'})

    def test_duplicate_case_identity_and_wrong_owner_are_rejected(self):
        for duplicate in (True, False):
            with self.subTest(duplicate=duplicate), tempfile.TemporaryDirectory() as tmp:
                directory = Path(tmp) / 'scenarios'
                shutil.copytree(scenario_directory(), directory)
                path = directory / 'SDK-US-001.json'
                document = json.loads(path.read_text())
                if duplicate:
                    document['cases'].append(deepcopy(document['cases'][0]))
                else:
                    document['cases'][0]['storyId'] = 'SDK-US-002'
                path.write_text(json.dumps(document))
                with self.assertRaises(ValueError):
                    story_documents(directory)


if __name__ == '__main__':
    unittest.main()
