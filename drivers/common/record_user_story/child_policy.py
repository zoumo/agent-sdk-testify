"""The explicitly authorized one-child synthetic read case, without SDK imports."""
import json

CHILD_FILE = 'child-fixture.txt'
CHILD_TEXT = 'SYNTHETIC_CHILD_TOKEN\n'
CHILD_ROLE = 'fixture_reader'


def child_task(provider, *, strict=False, background=False):
    tool = 'fixture_read' if provider == 'codex' else 'Read'
    if strict:
        gate = ('First call mcp__child_gate__wait_for_root with no arguments and wait for its success. '
                if provider == 'codex' and background else '')
        return (gate + f'Use {tool} exactly once to read only child-fixture.txt in the inherited working directory. '
                'Return exactly the text returned by that read. Do not create agents or descendants. '
                'Do not use shell, network, search, writes, or any other file or tool.')
    return (f'Use {tool} exactly once to read only child-fixture.txt in the inherited working directory. '
            'Return exactly SYNTHETIC_CHILD_TOKEN. Do not create agents or descendants. '
            'Do not use shell, network, search, writes, or any other file or tool.')


def role_file(*, strict=False, background=False):
    return ('name="fixture_reader"\n'
            'description="Read the single synthetic child fixture with fixture_read."\n'
            'developer_instructions=' + json.dumps(child_task('codex', strict=strict, background=background)) + '\n')


def check_output(plan, obj, state):
    """Observe environment activity without treating it as a capture error."""
    subtype, method = obj.get('subtype', ''), obj.get('method', '')
    if subtype.startswith('hook_') or method.startswith('hook/'):
        state['hookEvents'] = state.get('hookEvents', 0) + 1
    if subtype.startswith('task_') or obj.get('parent_tool_use_id'):
        state['childEvents'] = state.get('childEvents', 0) + 1
