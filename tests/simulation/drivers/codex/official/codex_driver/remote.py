"""Two official-SDK connections to one owned native WebSocket service.

Each connection has its own immutable Go capture. Closing the first connection
must leave the same service process alive and able to answer the second one.
"""
import hashlib
import json
from pathlib import Path

from record_user_story.adapters.common import serial
from record_user_story.environment import read_regular

REQUIRED_PHASE_FILES = frozenset(('remote-service.json', 'remote-probe/raw/raw.jsonl',
    'remote-probe/raw/manifest.json', 'remote-probe/raw/transport.json', 'remote-probe/raw/stderr.raw'))




def verify_phase_files(root, files):
    root = Path(root)
    if not REQUIRED_PHASE_FILES.issubset(files):
        raise ValueError('remote phase evidence is incomplete')
    for name, digest in files.items():
        relative = Path(name)
        if (relative.is_absolute() or '..' in relative.parts or
                (name != 'remote-service.json' and relative.parent != Path('remote-probe/raw'))):
            raise ValueError('remote phase evidence path escaped its scope')
        path = root / relative
        if path.is_symlink() or hashlib.sha256(read_regular(path, None)).hexdigest() != digest:
            raise ValueError('remote phase recording changed')


def codex(plan, report, flow):
    """Case behavior only. Go owns the service and both protocol peers."""
    from openai_codex.client import CodexClient, CodexConfig
    from openai_codex.errors import TransportClosedError

    def client(phase):
        peer = plan['_peer'] if phase == 'main' else plan['_peer']['probe']
        return CodexClient(config=CodexConfig(
            launch_args_override=tuple([peer['command'], *peer.get('args', [])]),
            cwd=plan.get('_fixtureProject', plan['project']), client_name='official-python-remote-case',
            client_title='Remote connection close'))

    with client('main') as first:
        report['receipts'].append({'remote/initialize': serial(first.initialize())})
        started = first.thread_start({'cwd': plan['project'], 'model': plan['model'], 'ephemeral': False,
            'sandbox': 'workspace-write', 'approvalPolicy': 'on-request',
            'baseInstructions': 'Complete the supplied synthetic task. Be concise.'})
        identity = started.thread.id
        report['receipts'].append({'remote/thread-start': serial(started)})
        if started.model != plan['model']:
            raise ValueError('remote native model differs from requested model')
        for prompt in flow.inputs():
            flow.submit()
            turn = first.turn_start(identity, prompt)
            report['inputs'] += 1
            report['receipts'].append({'remote/input-submitted': serial(turn)})
            while True:
                event = first.next_turn_notification(turn.turn.id)
                report['events'] += 1
                if event.method == 'turn/completed':
                    native = serial(event.payload)
                    if (native.get('threadId') != identity or native.get('turn', {}).get('id') != turn.turn.id
                            or native['turn']['status'] != 'completed'):
                        raise ValueError('remote terminal identity or status differs')
                    flow.result()
                    report['results'] += 1
                    report['receipts'].append({'remote/turn-completed': native})
                    break
    try:
        first.turn_start(identity, 'This closed-client probe must never be sent.')
    except TransportClosedError:
        pass
    else:
        raise ValueError('closed official SDK client accepted a submission')
    with client('probe') as second:
        report['receipts'].append({'remote/probe-initialize': serial(second.initialize())})
        saved = serial(second.thread_read(identity, include_turns=True))
        if saved.get('thread', {}).get('id') != identity or not saved['thread'].get('turns'):
            raise ValueError('second connection could not read the completed remote thread')
        report['receipts'].append({'remote/probe-read': saved})
    report['receipts'].append({'remote-close': {'firstClientRejected': True,
        'sameServiceSurvived': True, 'secondConnectionReadSucceeded': True}})
