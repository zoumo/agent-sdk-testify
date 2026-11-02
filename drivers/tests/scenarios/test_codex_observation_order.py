"""Synthetic continuation evidence: no idle notification is required after a result."""
from copy import deepcopy
import unittest
import support
from record_user_story.continuation import require_codex_continuation
from record_user_story.scenarios import Untriggered


def evidence():
    frames = []
    def add(direction, **wire): frames.append(dict(direction=direction, epoch=1, wire=wire))
    add('c2s', id=0, method='initialize', params={})
    add('s2c', id=0, result={})
    add('c2s', id=1, method='thread/start', params={})
    add('s2c', id=1, result={'thread': {'id': 'root'}})
    receipts = [{'thread/start': {'thread': {'id': 'root'}}}]
    for n in (1, 2):
        turn = {'id': str(n), 'status': 'completed'}
        add('c2s', id=n+1, method='turn/start', params={'threadId': 'root'})
        add('s2c', id=n+1, result={'turn': {'id': str(n)}})
        add('s2c', method='thread/status/changed', params={'threadId': 'root', 'status': {'type': 'idle'}})
        add('s2c', method='turn/completed', params={'threadId': 'root', 'turn': turn})
        receipts.extend([{'input-submitted': {'turn': {'id': str(n)}}}, {'turn/completed': {'threadId': 'root', 'turn': turn}}])
    return frames, {'receipts': receipts}


class CodexObservationOrderTests(unittest.TestCase):
    def test_native_idle_before_result_and_no_idle_are_valid(self):
        frames, report = evidence()
        require_codex_continuation(frames, report)
        require_codex_continuation([f for f in frames if f['wire'].get('method') != 'thread/status/changed'], report)

    def test_wrong_identity_missing_results_reopen_and_early_submit_fail(self):
        def thread(fs, r): fs[8]['wire']['params']['threadId']='other'
        def turn(fs, r): fs[11]['wire']['params']['turn']['id']='1'
        def missing(fs, r): fs.pop()
        def failed(fs, r): fs[11]['wire']['params']['turn']['status']='failed'
        def error(fs, r): fs[11]['wire']['params']['turn']['error']={'message':'bad'}
        def reopen(fs, r): fs.insert(8,deepcopy(fs[2]))
        def resume(fs, r): fs.insert(8,{'direction':'c2s','wire':{'method':'thread/resume'}})
        def detach(fs, r): fs.insert(8,{'direction':'c2s','wire':{'method':'thread/unsubscribe'}})
        def early(fs, r): fs.insert(7,fs.pop(8))
        def reconnect(fs, r): fs[8]['epoch']=2
        def ack(fs, r): fs[9]['wire']['result']['turn']['id']='other'
        def lost_ack(fs, r): fs.pop(9)
        def rejected(fs, r): fs[9]['wire']={'id':3,'error':{'code':-1}}
        def borrowed(fs, r): r['receipts'][-1]['turn/completed']['threadId']='other'
        for mutate in (thread,turn,missing,failed,error,reopen,resume,detach,early,reconnect,ack,lost_ack,rejected,borrowed):
            with self.subTest(mutate=mutate.__name__):
                fs,r=deepcopy(evidence());mutate(fs,r)
                with self.assertRaises(Untriggered): require_codex_continuation(fs,r)
