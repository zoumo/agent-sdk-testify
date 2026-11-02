"""Held approval evidence must be native, session-bound and inside its request span."""
from copy import deepcopy
import json
from pathlib import Path
import tempfile
import unittest
from support import BOOTSTRAP
from record_user_story.drivers import observe_wire
from record_user_story.scenarios import Untriggered

class HeldPermissionEvidenceTests(unittest.TestCase):
    def test_original_permission_control_event_restore_and_terminal_are_correlated(self):
        def frame(direction, **wire): return {'direction': direction, 'wire': wire}
        original = [
            frame('s2c', type='system', subtype='init', session_id='root', permissionMode='default'),
            frame('s2c', type='assistant', session_id='root', parent_tool_use_id=None,
                  message={'content':[{'type':'tool_use','id':'read','name':'Read'}]}),
            frame('s2c', type='control_request', request_id='permission', request={'subtype':'can_use_tool','tool_name':'Read','tool_use_id':'read'}),
            frame('c2s', type='control_request', request_id='change', request={'subtype':'set_permission_mode','mode':'plan'}),
            frame('s2c', type='system', subtype='status', session_id='root', uuid='held-event', permissionMode='plan', status=None),
            frame('s2c', type='control_response', response={'subtype':'success','request_id':'change'}),
            frame('c2s', type='control_request', request_id='restore', request={'subtype':'set_permission_mode','mode':'default'}),
            frame('s2c', type='control_response', response={'subtype':'success','request_id':'restore'}),
            frame('c2s', type='control_response', response={'subtype':'success','request_id':'permission','response':{'behavior':'allow'}}),
            frame('s2c', type='result', session_id='root', is_error=False, uuid='result'),
        ]
        report = {'receipts': [
            {'held-permission-control':{'operation':'set_permission_mode','requested':'plan','previousMode':'default','acknowledged':True}},
            {'held-permission-event':{'native':{'subtype':'status','data':original[4]['wire']}}},
            {'held-permission-restore':{'mode':'default','acknowledged':True}},
            {'result':{'session_id':'root','is_error':False,'uuid':'result'}},
        ]}
        def wrong_session(fs): fs[4]['wire']['session_id']='child'
        def missing_session(fs): fs[4]['wire'].pop('session_id')
        def child_event(fs): fs[4]['wire']['parent_tool_use_id']='child-parent'
        def wrong_event(fs): fs[4]['wire']['uuid']='unobserved'
        def wrong_mode(fs): fs[4]['wire']['permissionMode']='acceptEdits'
        def no_ack(fs): fs[5]['wire']['response']['request_id']='other'
        def wrong_answer(fs): fs[8]['wire']['response']['request_id']='other'
        def early_answer(fs): fs.insert(4,fs.pop(8))
        def stale_event(fs): fs.insert(1,fs.pop(4))
        def no_restore(fs): fs[7]['wire']['response']['subtype']='error'
        def wrong_terminal(fs): fs[9]['wire']['session_id']='other'
        def wrong_terminal_uuid(fs): fs[9]['wire']['uuid']='unobserved-result'
        def cancelled(fs): fs.insert(8,frame('s2c',type='control_cancel_request',request_id='permission'))
        def denied_then_allowed(fs):
            denied=deepcopy(fs[8]);denied['wire']['response']['response']['behavior']='deny';fs.insert(4,denied)
        def answered_twice(fs): fs.insert(9,deepcopy(fs[8]))
        def requested_twice(fs): fs.insert(3,deepcopy(fs[2]))
        def child_request(fs): fs[1]['wire']['parent_tool_use_id']='child-parent'
        def unrelated_request(fs): fs[1]['wire']['session_id']='another-session'
        def wrong_tool(fs): fs[2]['wire']['request']['tool_use_id']='another-tool'
        def restore_error_then_success(fs):
            error=deepcopy(fs[7]);error['wire']['response']['subtype']='error';fs.insert(7,error)
        def wrong_restore_ack_mode(fs): fs[7]['wire']['response']['response']={'mode':'plan'}
        for mutate in [None,wrong_session,missing_session,child_event,wrong_event,wrong_mode,no_ack,wrong_answer,
                       early_answer,stale_event,no_restore,wrong_terminal,wrong_terminal_uuid,cancelled,
                       denied_then_allowed,answered_twice,requested_twice,child_request,unrelated_request,wrong_tool,
                       restore_error_then_success,wrong_restore_ack_mode]:
            with self.subTest(mutate=mutate), tempfile.TemporaryDirectory() as tmp:
                fs=deepcopy(original)
                if mutate: mutate(fs)
                raw=Path(tmp)/'raw';raw.mkdir()
                (raw/'raw.jsonl').write_text(''.join(json.dumps(f)+'\n' for f in fs))
                plan={'root':tmp,'provider':'claude','driverProfile':'deferred-approval','scenario':{}}
                if mutate:
                    with self.assertRaises(Untriggered): observe_wire(plan,deepcopy(report))
                else: observe_wire(plan,deepcopy(report))

        def no_restore_receipt(r): r['receipts'].pop(2)
        def wrong_restore_mode(r): r['receipts'][2]['held-permission-restore']['mode']='plan'
        def unacknowledged_restore(r): r['receipts'][2]['held-permission-restore']['acknowledged']=False
        def wrong_observed_mode(r): r['receipts'][1]['held-permission-event']['native']['data']['permissionMode']='default'
        def wrong_observed_parent(r): r['receipts'][1]['held-permission-event']['native']['data']['parent_tool_use_id']='child'
        def wrong_sdk_terminal(r): r['receipts'][3]['result']['uuid']='unobserved-result'
        for mutate in [no_restore_receipt,wrong_restore_mode,unacknowledged_restore,
                       wrong_observed_mode,wrong_observed_parent,wrong_sdk_terminal]:
            with self.subTest(receipt_mutation=mutate), tempfile.TemporaryDirectory() as tmp:
                r=deepcopy(report);mutate(r)
                raw=Path(tmp)/'raw';raw.mkdir()
                (raw/'raw.jsonl').write_text(''.join(json.dumps(f)+'\n' for f in original))
                plan={'root':tmp,'provider':'claude','driverProfile':'deferred-approval','scenario':{}}
                with self.assertRaises(Untriggered): observe_wire(plan,r)
