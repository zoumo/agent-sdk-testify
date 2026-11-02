"""Synthetic SDK and wire tests for cancellation-gated native shutdown."""
import asyncio
from copy import deepcopy
from dataclasses import dataclass
import json
from pathlib import Path
import tempfile
from types import ModuleType
import unittest
from unittest.mock import patch

from support import BOOTSTRAP
from claude_driver.adapter import claude
from record_user_story.drivers import require_interrupt_wire, require_interrupt_close
from record_user_story.environment import stdin_eof_action
from record_user_story.scenarios import Flow, recipe, Untriggered


def wire_evidence():
    def f(direction, **wire): return {'direction': direction, 'wire': wire}
    terminal = dict(type='result', session_id='root', uuid='terminal', is_error=True,
                    subtype='error_during_execution', terminal_reason='aborted_streaming', queued_turn_count=0)
    idle = dict(type='system', subtype='session_state_changed', session_id='root', uuid='idle', state='idle')
    frames = [
        f('c2s', type='user', session_id='default', message={'role': 'user', 'content': 'synthetic'}),
        f('s2c', type='system', subtype='init', session_id='root'),
        f('c2s', type='control_request', request_id='interrupt', request={'subtype': 'interrupt'}),
        f('s2c', type='control_response', response={'request_id': 'interrupt', 'subtype': 'success'}),
        f('s2c', **terminal), f('s2c', **idle),
    ]
    report = {'receipts': [{'interrupt': 'acknowledged'}, {'interrupt-session': 'root'}, {'result': terminal}],
              'interruptClose': {'ready': True, 'sessionId': 'root', 'idle': {'subtype': 'session_state_changed', 'data': idle}}}
    return frames, report


class InterruptLifecycleTests(unittest.TestCase):

    def test_sdk_idle_cannot_borrow_a_matching_raw_uuid(self):
        for key, value in [('session_id', 'other'), ('parent_tool_use_id', 'child'), ('state', 'running')]:
            with self.subTest(key=key):
                frames, saved = wire_evidence()
                saved = deepcopy(saved)
                saved['interruptClose']['idle']['data'][key] = value
                with self.assertRaises(Untriggered): require_interrupt_wire(frames, saved)
        frames, saved = wire_evidence()
        saved['interruptClose']['idle']['subtype'] = 'status'
        with self.assertRaises(Untriggered): require_interrupt_wire(frames, saved)

    def test_policy_only_allows_explicit_claude_interrupt(self):
        self.assertEqual(stdin_eof_action({}), 'close')
        self.assertEqual(stdin_eof_action({'provider': 'claude', 'driverProfile': 'interrupt', 'stdinEOFAction': 'interrupt'}), 'interrupt')
        for change in ({'provider': 'codex'}, {'driverProfile': 'continuation'}, {'stdinEOFAction': 'ignore'}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                stdin_eof_action(dict(provider='claude', driverProfile='interrupt', stdinEOFAction='interrupt') | change)

    def test_raw_order_and_identity_are_required(self):
        def swap(fs): fs[3], fs[4] = fs[4], fs[3]
        def early_idle(fs): fs.insert(3, fs.pop())
        def wrong_ack(fs): fs[3]['wire']['response']['request_id'] = 'other'
        def rejected(fs): fs[3]['wire']['response']['subtype'] = 'error'
        def result_session(fs): fs[4]['wire']['session_id'] = 'child'
        def child_result(fs): fs[4]['wire']['parent_tool_use_id'] = 'child'
        def result_identity(fs): fs[4]['wire']['uuid'] = 'other'
        def result_error(fs): fs[4]['wire']['api_error_status'] = 500
        def result_success(fs): fs[4]['wire']['is_error'] = False
        def result_reason(fs): fs[4]['wire']['terminal_reason'] = 'completed'
        def queued(fs): fs[4]['wire']['queued_turn_count'] = 1
        def idle_session(fs): fs[5]['wire']['session_id'] = 'other'
        def idle_missing(fs): fs[5]['wire'].pop('session_id')
        def idle_child(fs): fs[5]['wire']['parent_tool_use_id'] = 'child'
        def idle_identity(fs): fs[5]['wire']['uuid'] = 'other'
        def extra_result(fs): fs.append(deepcopy(fs[4]))
        def extra_input(fs): fs.append(deepcopy(fs[0]))
        def missing_request(fs): fs.pop(2)
        for mutate in [None, swap, early_idle, wrong_ack, rejected, result_session, child_result,
                       result_identity, result_error, result_success, result_reason, queued, idle_session,
                       idle_missing, idle_child, idle_identity, extra_result, extra_input, missing_request]:
            with self.subTest(mutate=mutate):
                frames, saved = wire_evidence()
                if mutate:
                    mutate(frames)
                    with self.assertRaises(Untriggered): require_interrupt_wire(frames, saved)
                else:
                    require_interrupt_wire(frames, saved)

    def test_transport_success_is_not_inferred_from_cancellation(self):
        valid = dict(captureStatus='complete', reason='EOF', clientEOF=True, stdinEOFAction='interrupt', stdinEOFSignal='SIGINT',
                     nativeExitCode=0, nativeLaunched=True, reaped=True, cleanupComplete=True)
        for key, value in [(None, None), ('nativeExitCode', 1), ('nativeExitCode', False), ('nativeSignal', 'interrupt'), ('reason', 'canceled'),
                           ('stdinEOFSignal', ''), ('stdinEOFAction', 'close'), ('clientEOF', False),
                           ('reaped', False), ('cleanupComplete', False), ('captureStatus', 'failed'), ('nativeLaunched', False)]:
            with self.subTest(key=key), tempfile.TemporaryDirectory() as tmp:
                receipt = dict(valid)
                if key: receipt[key] = value
                root = Path(tmp); (root / 'raw').mkdir()
                (root / 'raw/transport.json').write_text(json.dumps(receipt))
                _, saved = wire_evidence()
                if key:
                    with self.assertRaises(RuntimeError): require_interrupt_close({'root': tmp}, saved, False)
                else:
                    require_interrupt_close({'root': tmp}, saved, False)
                    self.assertTrue(saved['interruptClose']['processClosed'])

    def test_mock_requires_full_consumption_and_eof_without_claiming_a_signal(self):
        valid = dict(fullConsumption=True, clientEOF=True, nativeLaunched=False)
        for key in [None, 'fullConsumption', 'clientEOF', 'nativeLaunched']:
            with self.subTest(key=key), tempfile.TemporaryDirectory() as tmp:
                receipt = dict(valid)
                if key: receipt[key] = not receipt[key]
                (Path(tmp) / 'replay-peer.json').write_text(json.dumps(receipt))
                _, saved = wire_evidence()
                if key:
                    with self.assertRaises(RuntimeError): require_interrupt_close({'root': tmp}, saved, True)
                else:
                    require_interrupt_close({'root': tmp}, saved, True)
                    self.assertNotIn('signal', saved['interruptClose'])

    def test_sdk_waits_for_same_session_idle_before_eof_and_cancels_unready_cleanup(self):
        for failure in [None, 'no-init', 'wrong-result-session', 'api-error', 'not-cancelled', 'successful-result',
                        'interrupt-rejected', 'missing-idle', 'wrong-idle-session', 'child-idle', 'extra-result', 'early-end-input']:
            with self.subTest(failure=failure):
                actions = []
                @dataclass
                class ResultMessage:
                    is_error: bool = failure != 'successful-result'
                    subtype: str = 'error_during_execution'
                    terminal_reason: str = 'completed' if failure == 'not-cancelled' else 'aborted_streaming'
                    session_id: str = 'other' if failure == 'wrong-result-session' else 'root'
                    uuid: str = 'terminal'
                    api_error_status: int = 500 if failure == 'api-error' else None
                class StreamEvent: pass
                class Stdin:
                    def close(self):
                        actions.append('EOF')
                        if proc.returncode is None: proc.returncode = 0
                class Process:
                    returncode = None
                    stdin = Stdin()
                    def terminate(self): actions.append('terminate'); self.returncode = 3
                    async def wait(self): actions.append('wait'); return self.returncode
                proc = Process()
                class Client:
                    def __init__(self, transport, **_): self.transport = transport; transport.proc = proc
                    async def __aenter__(self): return self
                    async def __aexit__(self, *_): await self.transport.close()
                    async def query(self, _):
                        if failure == 'early-end-input': await self.transport.end_input()
                    async def interrupt(self):
                        actions.append('interrupt-ack')
                        if failure == 'interrupt-rejected': raise ValueError('interrupt rejected')
                    async def receive_response(self):
                        if failure != 'no-init': yield {'subtype': 'init', 'data': {'session_id': 'root'}}
                        yield StreamEvent()
                        actions.append('terminal')
                        yield ResultMessage()
                    async def receive_messages(self):
                        if failure == 'extra-result': yield ResultMessage()
                        if failure == 'missing-idle': return
                        actions.append('idle')
                        yield {'subtype': 'session_state_changed', 'data': {
                            'state': 'idle', 'uuid': 'idle', 'session_id': 'other' if failure == 'wrong-idle-session' else 'root',
                            'parent_tool_use_id': 'child' if failure == 'child-idle' else None}}
                sdk = ModuleType('claude_agent_sdk')
                sdk.ClaudeSDKClient, sdk.ResultMessage, sdk.Transport = Client, ResultMessage, object
                sdk.ClaudeAgentOptions = sdk.PermissionResultAllow = sdk.PermissionResultDeny = lambda **kw: kw
                definition = recipe('claude', 'SDK-US-007', case_id='SDK-CASE-030')
                plan = dict(definition, project='/unused', model='fixture', stdinEOFAction='interrupt')
                saved = dict(inputs=0, results=0, events=0, requests=0, receipts=[])
                with patch.dict('sys.modules', {'claude_agent_sdk': sdk}):
                    call = claude(plan, saved, Flow(definition))
                    if failure:
                        with self.assertRaises((ValueError, RuntimeError, Untriggered)): asyncio.run(call)
                        self.assertLess(actions.index('terminate'), actions.index('EOF'))
                        self.assertLess(actions.index('wait'), actions.index('EOF'))
                        self.assertNotIn('interruptClose', saved)
                    else:
                        asyncio.run(call)
                        self.assertEqual(actions, ['interrupt-ack', 'terminal', 'idle', 'EOF'])
                        self.assertTrue(saved['interruptClose']['ready'])


if __name__ == '__main__': unittest.main()
