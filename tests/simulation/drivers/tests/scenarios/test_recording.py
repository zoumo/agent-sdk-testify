"""Offline safety tests. No CLI/model/network access."""
import hashlib
from pathlib import Path
import tempfile
import unittest

from support import BOOTSTRAP
from record_user_story.environment import private_json, read_regular, verify_recording
from record_user_story.child_policy import check_output


class SafetyTests(unittest.TestCase):


    def test_source_symlink_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'link'
            path.symlink_to('/etc/hosts')
            with self.assertRaises(OSError):
                read_regular(path)

    def test_private_json_never_overwrites(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'attempt.started'
            private_json(path, {'reserved': True})
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            with self.assertRaises(FileExistsError):
                private_json(path, {})


    def test_native_hook_child_and_extra_tool_events_are_observed(self):
        plan = {'provider': 'claude', 'story': 'SDK-US-006', 'childCase': True}
        state = {}
        root = {'type': 'assistant', 'message': {'content': [{'type': 'tool_use', 'name': 'Agent', 'id': 'one'}]}}
        check_output(plan, root, state)
        check_output(plan, {'type': 'system', 'subtype': 'task_started', 'task_id': 'child', 'tool_use_id': 'one'}, state)
        check_output(plan, {'type': 'assistant', 'parent_tool_use_id': 'one', 'message': {'content': [{'type': 'tool_use', 'name': 'Read', 'id': 'read'}]}}, state)
        for event in [
            {'type': 'assistant', 'message': {'content': [{'type': 'tool_use', 'name': 'Agent', 'id': 'two'}]}},
            {'type': 'assistant', 'parent_tool_use_id': 'one', 'message': {'content': [{'type': 'tool_use', 'name': 'Agent', 'id': 'nested'}]}},
            {'type': 'assistant', 'parent_tool_use_id': 'one', 'message': {'content': [{'type': 'tool_use', 'name': 'Bash', 'id': 'shell'}]}},
        ]:
            check_output(plan, event, state)
        check_output({'provider': 'claude', 'story': 'SDK-US-001'}, {'parent_tool_use_id': 'one'}, state)
        check_output(plan, {'method': 'hook/started'}, state)
        self.assertEqual(state['hookEvents'], 1)
        self.assertGreater(state['childEvents'], 0)


    def test_raw_replay_preserves_native_numeric_lexemes(self):
        for provider in ['codex', 'claude']:
            with self.subTest(provider=provider), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                (root / 'raw').mkdir()
                wire = b'{"opaque":0.123456789012345678901234567890,"id":9007199254740993}'
                data = b'{"ordinal":1,"direction":"s2c","channel":"protocol","epoch":1,"elapsedMs":0,"wire":' + wire + b'}\n'
                (root / 'raw/raw.jsonl').write_bytes(data)
                private_json(root / 'plan.json', {'provider': provider})
                private_json(root / 'manifest.json', {'files': {'raw.jsonl': hashlib.sha256(data).hexdigest()}})
                verify_recording(root)
                path = root / 'raw/raw.jsonl'
                self.assertIn(wire, path.read_bytes())
                verify_recording(root)
                (root / 'raw/raw.jsonl').write_bytes(data.replace(b'9007199254740993', b'9007199254740992'))
                with self.assertRaises(ValueError):
                    verify_recording(root)


if __name__ == '__main__':
    unittest.main()
