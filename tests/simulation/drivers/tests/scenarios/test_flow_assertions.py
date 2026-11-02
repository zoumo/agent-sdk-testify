"""Code integration checks for simulation flow evidence; no real captures."""
from types import SimpleNamespace
import unittest
from support import BOOTSTRAP
from record_user_story.stimuli import content


class FlowAssertionTests(unittest.TestCase):
    def test_topology_requires_native_parent_edges_and_overlapping_outputs(self):
        from record_user_story.topology import codex
        from record_user_story.scenarios import Untriggered
        def node(identity, parent, start, end):
            return {'id': identity, 'parentThreadId': parent, 'turns': [{'status': 'completed', 'startedAt': start, 'completedAt': end,
                    'items': [{'type': 'agentMessage', 'text': 'SYNTHETIC'}]}]}
        nodes = {'root': {'id': 'root', 'turns': [{'items': [{'tool': 'spawnAgent', 'receiverThreadIds': ['a','b']}]}]},
                 'a': node('a','root',1,4), 'b': node('b','root',2,5)}
        client = SimpleNamespace(thread_read=lambda identity, **_: {'thread': nodes[identity]})
        report = {'receipts': []}
        codex(client, 'root', report, lambda x: x, 'parallel')
        self.assertTrue(report['topology']['verified'])
        nodes['b']['turns'][0]['startedAt'] = 6
        with self.assertRaises(Untriggered):
            codex(client, 'root', {'receipts': []}, lambda x: x, 'parallel')
        nodes['b']['parentThreadId'] = 'a'
        codex(client, 'root', {'receipts': []}, lambda x: x, 'nested')
        nodes['b'].pop('parentThreadId')
        with self.assertRaises(Untriggered):
            codex(client, 'root', {'receipts': []}, lambda x: x, 'nested')

    def test_media_are_embedded_native_blocks_in_order(self):
        mixed = content('codex', 'before', 'mixed')
        self.assertEqual([item['type'] for item in mixed], ['text', 'image', 'text'])
        self.assertTrue(mixed[1]['url'].startswith('data:image/png;base64,'))
        doc = content('claude', 'read', 'document')[1]
        self.assertEqual(doc['type'], 'document')
        self.assertEqual(doc['source']['media_type'], 'application/pdf')
        with self.assertRaises(ValueError):
            content('codex', 'read', 'document')
