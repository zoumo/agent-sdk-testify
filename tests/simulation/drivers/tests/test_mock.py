"""Protocol conformance through the independent Go peer; no SDK, credentials or real CLI."""
import json
import os
from pathlib import Path
import signal
import shutil
import subprocess
import tempfile
import unittest

from peer_support import identity

BIN = identity()["path"]


def scenario(provider, *actions, provenance='synthetic'):
    frames = []
    for i, action in enumerate(actions):
        direction, wire = action[0], action[1:]
        frame = dict(ordinal=i + 1, channel='protocol')
        if provider == 'codex':
            frame.update(epoch=1, direction='c2s' if direction == '>' else 's2c')
            if direction == '!':
                frame['disconnect'] = True
            else:
                frame['wire'] = json.loads(wire)
        else:
            frame['direction'] = {'>': 'client', '<': 'server', '!': 'disconnect'}[direction]
            if direction != '!':
                frame['data'] = json.loads(wire)
        frames.append(frame)
    return dict(version=1, provenance=provenance, frames=frames)


class MockTests(unittest.TestCase):
    def test_unknown_provider_is_not_interpreted_as_claude(self):
        result = subprocess.run([BIN, 'protocol', 'mock', '--provider', 'another-agent', '--scenario', '/does-not-exist'],
                                input=b'', capture_output=True, timeout=3)
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, b'')

    def test_copied_binary_needs_no_python_sdk_or_checkout(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            launcher = root / 'agent-sdk-testify'
            shutil.copyfile(BIN, launcher)
            launcher.chmod(0o700)
            for provider in ('codex', 'claude'):
                path, receipt = root/(provider+'.json'), root/(provider+'-receipt.json')
                path.write_text(json.dumps(scenario(provider, '<{}')))
                result = subprocess.run([str(launcher), 'protocol', 'mock', '--provider', provider,
                                         '--scenario', str(path), '--receipt', str(receipt)],
                                        input=b'', capture_output=True, timeout=3, env={'PATH': '/missing'}, cwd=root)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, b'{}\n')
                self.assertTrue(json.loads(receipt.read_text())['fullConsumption'])
                self.assertEqual(json.loads(receipt.read_text())['peer'], 'Go native mock')

    def replay(self, provider, document, client='', args=()):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)/'scenario.json'
            path.write_text(document if isinstance(document, str) else json.dumps(document))
            result = subprocess.run([BIN, 'protocol', 'mock', '--provider', provider, '--scenario', str(path), *args],
                                    input=client.encode(), capture_output=True, timeout=5)
            if result.returncode not in (0, 4):
                raise ValueError(result.stderr.decode())
            return result.returncode, result.stdout.decode()

    def test_codex_duplex_ids_resolutions_and_rpc_errors(self):
        tests = [
            ('interleaved namespaces', ['>{"id":1,"method":"one"}', '>{"id":2,"method":"two"}',
              '<{"id":2,"result":{}}', '<{"id":1,"method":"question"}',
              '>{"id":1,"result":{"answer":"blue"}}', '<{"id":1,"result":{}}'],
             '{"id":"live","method":"one"}\n{"id":8,"method":"two"}\n{"id":1,"result":{"answer":"blue"}}\n'),
            ('resolved without answer', ['<{"id":"q","method":"approval","params":{"threadId":"child"}}',
              '<{"method":"serverRequest/resolved","params":{"threadId":"child","requestId":"q"}}'], ''),
            ('RPC error', ['>{"id":1,"method":"fault"}', '<{"id":1,"error":{"code":-32000,"message":"synthetic"}}'],
             '{"id":9007199254740993,"method":"fault"}\n'),
            ('future positional params', ['<{"id":"q","method":"future/request","params":[{"opaque":true}]}', '>{"id":"q","result":{}}'],
             '{"id":"q","result":{}}\n'),
        ]
        for name, frames, client in tests:
            with self.subTest(name=name):
                code, out = self.replay('codex', scenario('codex', *frames), client)
                self.assertEqual(code, 0)
                self.assertTrue(out)

    def test_codex_mismatch_duplicate_pending_and_extra_input_fail(self):
        base = ['>{"id":1,"method":"one","params":{"x":1}}', '<{"id":1,"result":{}}']
        cases = [
            ('EOF', base, ''),
            ('method', base, '{"id":9,"method":"other"}\n'),
            ('parameter', base, '{"id":9,"method":"one","params":{"x":2}}\n'),
            ('missing ID', base, '{"method":"one","params":{"x":1}}\n'),
            ('null ID', base, '{"id":null,"method":"one","params":{"x":1}}\n'),
            ('extra', base, '{"id":9,"method":"one","params":{"x":1}}\n{}\n'),
            ('duplicate live IDs', ['>{"id":1,"method":"one"}', '>{"id":2,"method":"two"}'], '{"id":9,"method":"one"}\n{"id":9,"method":"two"}\n'),
            ('unmatched response', ['<{"id":1,"result":{}}'], ''),
            ('pending client', ['>{"id":1,"method":"one"}'], '{"id":9,"method":"one"}\n'),
            ('pending reverse', ['<{"id":"q","method":"question"}'], ''),
            ('wrong reverse ID', ['<{"id":"q","method":"question"}', '>{"id":"q","result":{}}'], '{"id":"wrong","result":{}}\n'),
            ('late answer', ['<{"id":"q","method":"approval","params":{"threadId":"child"}}', '<{"method":"serverRequest/resolved","params":{"threadId":"child","requestId":"q"}}', '>{"id":"q","result":{}}'], '{"id":"q","result":{}}\n'),
            ('wrong resolution thread', ['<{"id":"q","method":"approval","params":{"threadId":"child"}}', '<{"method":"serverRequest/resolved","params":{"threadId":"root","requestId":"q"}}'], ''),
            ('reverse ID reuse', ['<{"id":"q","method":"question"}', '>{"id":"q","result":{}}', '<{"id":"q","method":"question"}'], '{"id":"q","result":{}}\n'),
            ('invalid JSON', base, '[1,2]\n'),
            ('trailing JSON', base, '{} {}\n'),
            ('oversize client', base, 'x' * ((2 << 20) + 1)),
        ]
        for name, frames, client in cases:
            with self.subTest(name=name), self.assertRaises((ValueError, OSError)):
                self.replay('codex', scenario('codex', *frames), client)

    def test_claude_ids_payload_fields_and_native_cancellation(self):
        actions = ['>{"type":"control_request","request_id":"old","request":{"request_id":"literal"}}',
                   '<{"type":"control_response","response":{"request_id":"old","payload":{"request_id":"literal"}}}',
                   '<{"type":"control_request","request_id":"permission","request":{}}',
                   '<{"type":"control_cancel_request","request_id":"permission"}']
        code, out = self.replay('claude', scenario('claude', *actions),
                                '{"type":"control_request","request_id":"dynamic","request":{"request_id":"literal"}}\n')
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(out.splitlines()[0])['response']['request_id'], 'dynamic')
        self.assertIn('"request_id": "literal"', out)
        for name, modified, client in [
            ('pending client', actions[:1], '{"type":"control_request","request_id":"new","request":{"request_id":"literal"}}\n'),
            ('pending server', actions[2:3], ''),
            ('unknown response', actions[1:2], ''),
            ('unknown cancellation', actions[3:], ''),
            ('wrong ID type', actions[:2], '{"type":"control_request","request_id":1,"request":{"request_id":"literal"}}\n'),
            ('payload ID changed', actions[:2], '{"type":"control_request","request_id":"new","request":{"request_id":"changed"}}\n'),
        ]:
            with self.subTest(name=name), self.assertRaises(ValueError):
                self.replay('claude', scenario('claude', *modified), client)

    def test_raw_jsonl_and_number_lexemes_are_preserved(self):
        value = '0.123456789012345678901234567890'
        for provider in ('codex', 'claude'):
            wire = '{"type":"notice","number":' + value + ',"id":9007199254740993,"request_id":"literal"}'
            raw = '{"ordinal":1,"direction":"s2c","channel":"protocol","epoch":1,"elapsedMs":0,"wire":' + wire + '}\n'
            with self.subTest(provider=provider):
                if provider == 'codex':
                    # An unrequested JSON-RPC response is correctly rejected;
                    # use a notification to check all other payload numbers.
                    wire = wire.replace('"id":9007199254740993,', '"opaqueId":9007199254740993,')
                    raw = raw.replace('"id":9007199254740993,', '"opaqueId":9007199254740993,')
                code, out = self.replay(provider, raw)
                self.assertEqual(code, 0)
                self.assertEqual(out, wire + '\n')
        for saved, live in [('true', '1'), ('9007199254740993', '9007199254740992')]:
            with self.subTest(saved=saved), self.assertRaises(ValueError):
                self.replay('codex', scenario('codex', '>{"x":' + saved + '}'), '{"x":' + live + '}\n')

    def test_invalid_scenarios_and_synthetic_fault_provenance(self):
        for provider in ('codex', 'claude'):
            for name in ('version', 'unknown wrapper', 'unknown frame', 'provenance', 'empty', 'ordinal', 'channel', 'direction', 'negative delay', 'excessive delay', 'scalar', 'oversize frame', 'too many', 'disconnect data', 'disconnect not last', 'recording no source', 'recording delay', 'recording disconnect'):
                doc = scenario(provider, '<{}')
                frame = doc['frames'][0]
                data_key = 'wire' if provider == 'codex' else 'data'
                delay_key = 'delayMs' if provider == 'codex' else 'delay_ms'
                if name == 'version': doc['version'] = 2
                elif name == 'unknown wrapper': doc['typo'] = True
                elif name == 'unknown frame': frame['typo'] = True
                elif name == 'provenance': doc['provenance'] = 'real'
                elif name == 'empty': doc['frames'] = []
                elif name == 'ordinal': frame['ordinal'] = 0
                elif name == 'channel': frame['channel'] = 'stdout'
                elif name == 'direction': frame['direction'] = 'ignored'
                elif name == 'negative delay': frame[delay_key] = -1
                elif name == 'excessive delay': frame[delay_key] = 300001
                elif name == 'scalar': frame[data_key] = None
                elif name == 'oversize frame': frame[data_key] = {'text': 'x' * (2 << 20)}
                elif name == 'too many': doc['frames'] = doc['frames'] * 10001
                elif name == 'disconnect data': frame['disconnect' if provider == 'codex' else 'direction'] = True if provider == 'codex' else 'disconnect'
                elif name == 'disconnect not last': doc = scenario(provider, '!', '<{}')
                elif name == 'recording no source': doc['provenance'] = 'recording'
                elif name == 'recording delay': doc.update(provenance='recording', source='manifest'); frame[delay_key] = 1
                elif name == 'recording disconnect': doc = scenario(provider, '!', provenance='recording'); doc['source'] = 'manifest'
                with self.subTest(provider=provider, name=name), self.assertRaises(ValueError):
                    self.replay(provider, doc)
            for text in ('{} {}', '{"version":1,"version":1}', '{"number":NaN}'):
                with self.subTest(text=text), self.assertRaises(ValueError):
                    self.replay(provider, text)

    def test_cli_exit_codes_receipt_timeout_and_cancellation(self):
        for provider in ('codex', 'claude'):
            for mode, frames, client, expected in [('complete', ['<{}'], '', 0), ('early EOF', ['>{}'], '', 1), ('extra', ['<{}'], '{}\n', 1), ('disconnect', ['!'], '', 4), ('invalid', [], '', 2)]:
                with self.subTest(provider=provider, mode=mode), tempfile.TemporaryDirectory() as tmp:
                    path, receipt = Path(tmp)/'scenario.json', Path(tmp)/'receipt.json'
                    path.write_text(json.dumps(scenario(provider, *frames)))
                    result = subprocess.run([BIN, 'protocol', 'mock', '--provider', provider, '--scenario', str(path), '--receipt', str(receipt)], input=client.encode(), capture_output=True, timeout=3)
                    self.assertEqual(result.returncode, expected, result.stderr)
                    self.assertEqual(receipt.exists(), expected == 0)
                    if expected == 0:
                        self.assertTrue(json.loads(receipt.read_text())['fullConsumption'])
            for mode in ('read', 'write', 'delay', 'eof'):
                with self.subTest(provider=provider, block=mode), tempfile.TemporaryDirectory() as tmp:
                    doc = scenario(provider, '>{}' if mode == 'read' else '<{}')
                    if mode == 'write': doc['frames'][0]['wire' if provider == 'codex' else 'data'] = {'text': 'x' * 1000000}
                    if mode == 'delay': doc['frames'][0]['delayMs' if provider == 'codex' else 'delay_ms'] = 1000
                    path = Path(tmp)/'scenario.json'; path.write_text(json.dumps(doc))
                    process = subprocess.Popen([BIN, 'protocol', 'mock', '--provider', provider, '--scenario', str(path), '--timeout', '0.1'], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                    self.assertEqual(process.wait(timeout=3), 3)
                    process.stdin.close(); process.stdout.close(); process.stderr.close()
            with tempfile.TemporaryDirectory() as tmp:
                path = Path(tmp)/'scenario.json'; path.write_text(json.dumps(scenario(provider, '<{}', '>{}')))
                process = subprocess.Popen([BIN, 'protocol', 'mock', '--provider', provider, '--scenario', str(path)], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                # The timeout handler is tested above; SIGTERM also must exit
                # without leaving a live protocol process.
                self.assertEqual(process.stdout.readline(), b'{}\n')
                process.send_signal(signal.SIGTERM)
                self.assertEqual(process.wait(timeout=3), 3)
                process.stdin.close(); process.stdout.close(); process.stderr.close()

    def test_launcher_handshake_exit_status(self):
        # Process completion belongs to the Go CLI, independently of SDK
        # shutdown (which may terminate its child).
        handshakes = {
            'codex': [
                '>{"id":91,"method":"initialize","params":{"clientInfo":{"name":"consumer","version":"1"}}}',
                '<{"id":91,"result":{"userAgent":"synthetic","codexHome":"/synthetic","platformFamily":"unix","platformOs":"linux"}}',
                '>{"method":"initialized"}',
            ],
            'claude': [
                '>{"type":"control_request","request_id":"init","request":{"subtype":"initialize","hooks":null,"forwardSubagentText":true}}',
                '<{"type":"control_response","response":{"subtype":"success","request_id":"init","response":{}}}',
            ],
        }
        for provider, handshake in handshakes.items():
            for mode, expected in (('complete', 0), ('extra', 1), ('disconnect', 4)):
                with self.subTest(provider=provider, mode=mode), tempfile.TemporaryDirectory() as tmp:
                    frames = handshake + (['!'] if mode == 'disconnect' else [])
                    client = ''.join(frame[1:] + '\n' for frame in handshake if frame.startswith('>'))
                    if mode == 'extra':
                        client += '{}\n'
                    path = Path(tmp)/'scenario.json'
                    path.write_text(json.dumps(scenario(provider, *frames)))
                    args = ['app-server'] if provider == 'codex' else []
                    result = subprocess.run(
                        [BIN, 'protocol', 'mock', '--provider', provider, '--scenario', str(path), *args],
                        input=client.encode(), capture_output=True, timeout=5,
                        env={'PATH': '/missing'},
                    )
                    self.assertEqual(result.returncode, expected, result.stderr)
                    if expected == 0:
                        self.assertEqual(result.stderr, b'')
                    else:
                        self.assertTrue(result.stderr)
                    expected_output = [json.loads(frame[1:]) for frame in handshake if frame.startswith('<')]
                    self.assertEqual([json.loads(line) for line in result.stdout.splitlines()], expected_output)

    def test_startup_arguments(self):
        for provider, args in [('codex', ['app-server', '--listen=stdio://']), ('claude', ['--output-format', 'stream-json', '--verbose', '--input-format', 'stream-json', '--permission-prompt-tool', 'stdio', '--include-partial-messages', '--model=x', '--resume=y'])]:
            self.assertEqual(self.replay(provider, scenario(provider, '<{}'), args=args)[0], 0)
            self.assertEqual(self.replay(provider, scenario(provider, '<{}'))[0], 0)
        for provider, args in [('codex', ['--listen', 'ws://localhost']), ('codex', ['app-server', 'app-server']), ('claude', ['--dangerously-skip-permissions']), ('claude', ['--verbose', '--verbose']), ('claude', ['--input-format', 'json']), ('claude', ['--output-format'])]:
            with self.subTest(args=args), self.assertRaises(ValueError):
                self.replay(provider, scenario(provider, '<{}'), args=args)

    def test_broken_output_pipe_is_an_error_without_success_receipt(self):
        # At the public process seam a closed output pipe exercises the I/O
        # failure previously injected via an in-process short-write stub.
        with tempfile.TemporaryDirectory() as tmp:
            path, receipt = Path(tmp)/'scenario.json', Path(tmp)/'receipt.json'
            path.write_text(json.dumps(scenario('codex', '<' + json.dumps({'text': 'x' * 1000000}))))
            read_fd, write_fd = os.pipe()
            os.close(read_fd)
            try:
                process = subprocess.Popen([BIN, 'protocol', 'mock', '--provider', 'codex', '--scenario', str(path),
                                            '--receipt', str(receipt)], stdin=subprocess.PIPE,
                                           stdout=write_fd, stderr=subprocess.PIPE)
                _, stderr = process.communicate(b'', timeout=5)
                self.assertNotEqual(process.returncode, 0, stderr)
                self.assertFalse(receipt.exists())
            finally:
                os.close(write_fd)
