"""Native SDK serialization and capture process wiring."""
from dataclasses import asdict, is_dataclass

def serial(value):
    if hasattr(value, 'model_dump'):
        return value.model_dump(mode='json', by_alias=True, exclude_none=True)
    if is_dataclass(value):
        return asdict(value)
    return value


def proxy_command(plan):
    peer = plan['_peer']
    return [peer['command'], *peer.get('args', [])]


def configuration_acknowledged(report, flow, requested, timing):
    evidence = report.setdefault('configurationEvidence', {'requests': [], 'observed': [], 'effectiveScope': 'unknown'})
    evidence['requests'].append({'requested': requested, 'acknowledged': True,
                                 'timing': timing, 'completedInputs': flow.completed})
    flow.trace.append({'step': 'configuration-acknowledged', 'timing': timing,
                       'completedInputs': flow.completed, 'requested': requested})


def observe_configuration(report, source, native):
    evidence = report.get('configurationEvidence')
    if evidence is None or not isinstance(native, dict):
        return
    if source == 'thread/settings/updated':
        # This is an observed thread setting, not proof of which execution used it.
        evidence['observed'].append({'source': source, 'scope': 'thread-settings', 'native': native})
    elif source == 'assistant' and isinstance(native.get('message'), dict) and native['message'].get('model'):
        evidence['observed'].append({'source': source, 'scope': 'assistant-message',
                                     'model': native['message']['model']})
