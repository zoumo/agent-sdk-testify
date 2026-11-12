"""Versioned, attempt-local driver configuration.

The material plan contains recording identities. This configuration contains
only the current driver, execution and peer locations.
"""
import json
from pathlib import Path


SCHEMA_VERSION = 1
_PATHS = ('root', 'workspace', 'home', 'config', 'raw', 'tmp', 'output')


def _peer(value):
    if not isinstance(value, dict):
        raise ValueError('official driver runtime peer is missing')
    _command(value, 'peer')
    if 'probe' in value:
        _command(value['probe'], 'peer probe')
    return value


def _command(value, name):
    if not isinstance(value, dict) or not isinstance(value.get('command'), str):
        raise ValueError(f'official driver runtime {name} command is missing')
    if not Path(value['command']).is_absolute():
        raise ValueError(f'official driver runtime {name} command must be absolute')
    args = value.get('args', [])
    if not isinstance(args, list) or any(not isinstance(item, str) for item in args):
        raise ValueError(f'official driver runtime {name} args are invalid')
    env = value.get('env')
    if env is not None and (not isinstance(env, dict)
                            or any(not isinstance(k, str) or not isinstance(v, str)
                                   for k, v in env.items())):
        raise ValueError(f'official driver runtime {name} env is invalid')


def apply_runtime(plan, request):
    path = request.get('runtime')
    if not isinstance(path, str) or not Path(path).is_absolute():
        raise ValueError('official driver requires a runtime configuration')
    runtime = json.loads(Path(path).read_text())
    if runtime.get('schemaVersion') != SCHEMA_VERSION:
        raise ValueError('unsupported official driver runtime configuration version')
    for name in _PATHS:
        value = runtime.get(name)
        if not isinstance(value, str) or not Path(value).is_absolute():
            raise ValueError(f'official driver runtime requires absolute {name}')
    if not isinstance(runtime.get('replay'), bool):
        raise ValueError('official driver runtime requires replay')
    if 'seedRoot' in runtime and (not isinstance(runtime['seedRoot'], str)
                                  or not Path(runtime['seedRoot']).is_absolute()):
        raise ValueError('official driver runtime seedRoot must be absolute')
    peer = _peer(runtime.get('peer'))
    results = runtime.get('agentResults', {})
    if (not isinstance(results, dict)
            or any(not isinstance(key, str) or not isinstance(value, str)
                   or not Path(value).is_absolute() for key, value in results.items())):
        raise ValueError('official driver runtime agentResults is invalid')
    plan['_runtime'] = runtime
    plan['root'] = runtime['root']
    plan['home'] = runtime['home']
    plan['config'] = runtime['config']
    plan['raw'] = runtime['raw']
    plan['tmp'] = runtime['tmp']
    plan['_executionWorkspace'] = runtime['workspace']
    plan['_seedRoot'] = runtime.get('seedRoot')
    plan['_agentResults'] = results
    plan['_replay'] = runtime['replay']
    plan['_peer'] = peer
    return runtime


def execution_workspace(plan):
    workspace = plan.get('_executionWorkspace')
    if workspace is None and plan.get('_nativeCase'):
        raise ValueError('official driver has no runtime execution workspace')
    return Path(workspace or plan.get('_fixtureProject') or plan['project'])


def protocol_project(plan):
    return Path(plan.get('_recordedWorkspace') or plan.get('_fixtureProject') or plan['project'])
