"""History acceptance rejects cursor loops, missing pages and altered slices."""
import unittest

from record_user_story.history import pages, claude_slices
from record_user_story.scenarios import Untriggered


class HistoryTests(unittest.TestCase):
    def test_native_cursor_is_opaque_and_order_is_preserved(self):
        calls = []
        def request(method, params):
            calls.append(dict(params))
            return ({'data': [{'id': 'z'}], 'nextCursor': 'opaque/+=='} if 'cursor' not in params
                    else {'data': [{'id': 'a'}], 'nextCursor': None})
        self.assertEqual(pages(request, 'list', {'limit': 1}, require_multiple=True), [{'id': 'z'}, {'id': 'a'}])
        self.assertEqual(calls, [{'limit': 1}, {'limit': 1, 'cursor': 'opaque/+=='}])

    def test_rejects_invalid_or_nonprogressing_history(self):
        for responses, error in [
            ([{'data': [{'id': 'a'}], 'nextCursor': None}], Untriggered),
            ([{'data': [{'id': 'a'}], 'nextCursor': 'same'}]*2, ValueError),
            ([{'data': [{'id': 'a'}], 'nextCursor': 'next'}, {'data': [{'id': 'a'}]}], ValueError),
            ([{'data': [], 'nextCursor': 'next'}], ValueError),
            ([{'data': [{'id': ''}]}], ValueError),
        ]:
            with self.subTest(responses=responses), self.assertRaises(error):
                source = iter(responses)
                pages(lambda *_: next(source), 'list', {}, require_multiple=True)

    def test_slices_include_empty_tail_and_reject_reorder(self):
        for corrupt in [False, True]:
            source = [{'uuid': 'one'}, {'uuid': 'two'}]
            def get_messages(identity, *, directory, limit, offset=0):
                if corrupt and limit == 1:
                    return list(reversed(source))[offset:offset+limit]
                return source[offset:offset+limit]
            report = {'receipts': []}
            if corrupt:
                with self.assertRaises(ValueError):
                    claude_slices(get_messages, 'session', '/project', report)
            else:
                claude_slices(get_messages, 'session', '/project', report)
                self.assertEqual(report['receipts'][-2]['history-slice']['messages'], [])
