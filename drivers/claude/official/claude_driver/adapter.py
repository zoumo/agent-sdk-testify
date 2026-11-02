"""Native interactions driven exclusively by the pinned official Python SDKs."""
import asyncio
import json
from pathlib import Path

from record_user_story.policy import driver_profile
from record_user_story.scenarios import Untriggered
from record_user_story.stimuli import content
from record_user_story.environment import stdin_eof_action


from record_user_story.adapters.common import serial, proxy_command, configuration_acknowledged


def cancelled_result(native, session_id):
    return (bool(session_id) and native.get('session_id') == session_id
            and native.get('parent_tool_use_id') is None and native.get('is_error') is True
            and native.get('subtype') == 'error_during_execution'
            and native.get('terminal_reason') in ('aborted_streaming', 'aborted_tools')
            and native.get('api_error_status') is None)


async def claude(plan, report, flow):
    from claude_agent_sdk import ClaudeSDKClient, ClaudeAgentOptions, Transport, PermissionResultAllow, PermissionResultDeny, ResultMessage
    profile = driver_profile(plan)
    interrupt_close = stdin_eof_action(plan) == 'interrupt'
    used = set()
    client = None
    parameters = flow.definition.get('parameters', {})
    mcp_ready = asyncio.Event()
    held_event = asyncio.Event()
    permission_held = False
    session_context = {}
    held_observation = {}

    def observe_session(native):
        data = native.get('data', native)
        if native.get('subtype') == 'init':
            session_context.update(sessionId=data.get('session_id'), mode=data.get('permissionMode'))
        return data

    class RawTransport(Transport):
        def __init__(self):
            self.proc = None
            self.interrupt_ready = False

        async def connect(self):
            self.proc = await asyncio.create_subprocess_exec(*proxy_command(plan), stdin=asyncio.subprocess.PIPE,
                            stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL)

        async def write(self, data):
            self.proc.stdin.write(data.encode())
            await self.proc.stdin.drain()
            if parameters.get('applicationTool'):
                for line in data.splitlines():
                    value = json.loads(line)
                    reply = value.get('response', {}).get('response', {}).get('mcp_response', {})
                    if isinstance(reply.get('result', {}).get('tools'), list):
                        # Native tools/list has been answered and fully written.
                        # This barrier prevents the first input racing SDK MCP setup.
                        mcp_ready.set()


        async def read_messages(self):
            from record_user_story.stream import json_messages
            async for message in json_messages(self.proc.stdout):
                # The control callback can run before the consumer drains init.
                # Keep the original native context without counting it as an event.
                observe_session(message)
                yield message

        async def end_input(self):
            if interrupt_close and not self.interrupt_ready:
                await self.abort_capture()
                raise RuntimeError('interrupt close requested before acknowledged cancellation and idle')
            if self.proc and self.proc.stdin:
                self.proc.stdin.close()

        async def abort_capture(self):
            # Keep stdin open while canceling the recorder. Otherwise cleanup
            # itself could trigger the success-only EOF -> native SIGINT path.
            if self.proc and self.proc.returncode is None:
                self.proc.terminate()
                await asyncio.wait_for(self.proc.wait(), 10)
            if self.proc and self.proc.stdin:
                self.proc.stdin.close()

        def is_ready(self):
            return self.proc is not None and self.proc.returncode is None

        async def close(self):
            if interrupt_close and not self.interrupt_ready:
                await self.abort_capture()
                return
            await self.end_input()
            if self.proc and self.proc.returncode is None:
                try:
                    await asyncio.wait_for(self.proc.wait(), 5)
                except asyncio.TimeoutError:
                    if interrupt_close:
                        await self.abort_capture()
                        raise RuntimeError('capture process did not finish after client EOF')
                    self.proc.terminate()
                    await asyncio.wait_for(self.proc.wait(), 5)
            if interrupt_close and self.proc and self.proc.returncode != 0:
                raise RuntimeError('interrupt capture process exited unsuccessfully')

    async def permission(tool, values, context):
        nonlocal permission_held
        report['requests'] += 1
        if tool == 'AskUserQuestion':
            answers = {}
            if len(values.get('questions', [])) != parameters.get('questionCount', len(values.get('questions', []))):
                flow.issues.append('native question count differs from target')
            for question in values.get('questions', []):
                labels = [item.get('label') for item in question.get('options', [])]
                choice = flow.definition['answers'].get('choice', 'Ready')
                if labels and choice not in labels and not parameters.get('freeText'):
                    choice = labels[0]
                answers[question['question']] = choice
            flow.request('question')
            used.add(tool)
            return PermissionResultAllow(updated_input={**values, 'answers': answers})
        if parameters.get('approvalKind') and tool == ('Write' if parameters['approvalKind'] == 'file' else 'Bash'):
            flow.request('approval_answer')
            if parameters['approvalDecision'] == 'deny':
                return PermissionResultDeny(message='Synthetic denial; do not retry')
            return PermissionResultAllow(updated_input=values)
        if tool in flow.definition['requiredRequests']:
            flow.request(tool)
        if profile == 'deferred-approval' and tool == 'Read' and tool not in used:
            previous = session_context.get('mode')
            if not session_context.get('sessionId') or not previous:
                raise Untriggered('native session and initial permission mode were not observed')
            target = 'default' if previous == 'plan' else 'plan'
            session_context['heldMode'] = target
            permission_held = True
            changed = False
            try:
                await client.set_permission_mode(target)
                changed = True
                flow.request('held-control')
                report['receipts'].append({'held-permission-control': {
                    'operation': 'set_permission_mode', 'requested': target,
                    'previousMode': previous, 'acknowledged': True}})
                try:
                    await asyncio.wait_for(held_event.wait(), 5)
                except asyncio.TimeoutError:
                    pass  # Missing native activity remains a case gap.
                if held_event.is_set():
                    flow.request('held-event')
                    report['receipts'].append({'held-permission-event': held_observation})
            finally:
                if changed:
                    await client.set_permission_mode(previous)
                    report['receipts'].append({'held-permission-restore': {'mode': previous, 'acknowledged': True}})
                permission_held = False
        used.add(tool)
        return PermissionResultAllow(updated_input=values)

    options = ClaudeAgentOptions(cwd=plan.get('_fixtureProject', plan['project']),
                can_use_tool=permission, include_partial_messages=True)
    if profile == 'history':
        # Use the official option so the captured initialization matches the
        # Go Session's explicit request for native child detail forwarding.
        options.forward_subagent_text = True
    if parameters.get('applicationTool'):
        from claude_agent_sdk import tool, create_sdk_mcp_server
        @tool('fixture_read', 'Read only the synthetic numbers.txt file.', {'path': str})
        async def fixture_read(arguments):
            if arguments.get('path') != 'numbers.txt':
                raise ValueError('application fixture tool accepts only numbers.txt')
            flow.request('fixture_read')
            report['requests'] += 1
            project = Path(plan.get('_fixtureProject', plan['project']))
            return {'content': [{'type': 'text', 'text': (project / 'numbers.txt').read_text()}]}
        options.mcp_servers = {'fixture': create_sdk_mcp_server('fixture', tools=[fixture_read])}

    transport = RawTransport()
    async with ClaudeSDKClient(options=options, transport=transport) as client:
        if parameters.get('applicationTool'):
            ready = asyncio.create_task(mcp_ready.wait())
            exited = asyncio.create_task(transport.proc.wait())
            try:
                await asyncio.wait((ready, exited), return_when=asyncio.FIRST_COMPLETED)
                if not mcp_ready.is_set():
                    raise RuntimeError('native CLI closed before SDK MCP tools/list handshake')
            finally:
                ready.cancel()
                exited.cancel()
                await asyncio.gather(ready, exited, return_exceptions=True)
            report['receipts'].append({'application-MCP-ready': 'native tools/list response fully written before first input'})

        async def variant_configuration():
            if parameters.get('setting') == 'model':
                await client.set_model(plan['model'] or None)
                requested = {'subtype': 'set_model', 'model': plan['model'] or None}
            else:
                requested = {'subtype': 'set_max_thinking_tokens', 'max_thinking_tokens': 1024}
                if parameters.get('setting') == 'display':
                    requested['thinking_display'] = 'omitted'
                await client._query._send_control_request(requested)
            configuration_acknowledged(report, flow, requested, parameters['configurationTiming'])
            report['receipts'].append({'variant-configuration': {'timing': parameters['configurationTiming'], 'requested': requested, 'acknowledged': True, 'observed': None}})
        if parameters.get('configurationTiming') == 'before-first-input' and parameters.get('setting') != 'effort':
            await variant_configuration()
        if flow.definition['configuration']:
            await client.set_model(plan['model'] or None)
            requested = {'subtype': 'set_model', 'model': plan['model'] or None}
            report['receipts'].append({'set_model': {'requested': requested, 'acknowledged': True}})
            configuration_acknowledged(report, flow, requested, 'before-first-input')
        for index, prompt in enumerate(flow.inputs()):
            if index == 1 and parameters.get('configurationTiming') == 'between-inputs':
                await variant_configuration()
            flow.submit()
            if profile == 'structured-input' or parameters.get('inputKind'):
                async def structured_input():
                    yield {'type': 'user', 'message': {'role': 'user', 'content': content('claude', prompt, parameters.get('inputKind'))}, 'parent_tool_use_id': None}
                await client.query(structured_input())
            else:
                if profile == 'history':
                    await client.query(prompt, session_id='')
                else:
                    await client.query(prompt)
            report['inputs'] += 1
            report['receipts'].append({'input-submitted': {'written': True, 'completionObserved': False}})
            interrupted = False
            configured_during = False
            async for event in client.receive_response():
                report['events'] += 1
                native = serial(event)
                data = observe_session(native) if isinstance(native, dict) else {}
                if (permission_held and native.get('subtype') == 'status'
                        and data.get('session_id') == session_context.get('sessionId')
                        and data.get('permissionMode') == session_context.get('heldMode')
                        and data.get('uuid') and data.get('parent_tool_use_id') is None):
                    held_observation.update(native=native)
                    held_event.set()
                if parameters.get('topology'):
                    report.setdefault('topologyEvents', []).append({'kind': type(event).__name__, 'native': serial(event)})
                if type(event).__name__ == 'StreamEvent' and parameters.get('configurationTiming') == 'during-execution' and not configured_during:
                    await variant_configuration()
                    configured_during = True
                action = flow.progress() if type(event).__name__ == 'StreamEvent' else None
                if action == 'submit-supplement':
                    await client.query(plan['prompts'][1])
                    flow.action(action, 'written')
                    report['inputs'] += 1
                    interrupted = True
                if action == 'interrupt':
                    if interrupt_close and not session_context.get('sessionId'):
                        raise ValueError('interrupt session identity was not observed')
                    await client.interrupt()
                    flow.action(action, 'accepted')
                    report['receipts'].append({'interrupt': 'acknowledged'})
                    if interrupt_close:
                        report['receipts'].append({'interrupt-session': session_context['sessionId']})
                    interrupted = True
                if isinstance(event, ResultMessage):
                    flow.result()
                    report['results'] += 1
                    report['receipts'].append({'result': serial(event)})
                    if event.is_error:
                        # Claude reports an acknowledged cancellation as an error
                        # result. Accept only its explicit cancellation reasons
                        # after this scenario's interrupt, retaining the raw result.
                        cancelled = (profile == 'interrupt' and interrupted
                                     and event.subtype == 'error_during_execution'
                                     and getattr(event, 'terminal_reason', None) in ('aborted_streaming', 'aborted_tools')
                                     and getattr(event, 'api_error_status', None) is None)
                        if interrupt_close:
                            cancelled = cancelled and cancelled_result(serial(event), session_context.get('sessionId'))
                        if not cancelled:
                            raise ValueError('native result is_error')
                        report['receipts'].append({'interrupted-result': {'terminalReason': getattr(event, 'terminal_reason', None)}})
                    elif interrupt_close:
                        raise ValueError('interrupt did not produce a native cancellation result')
            if interrupt_close:
                if not interrupted or not report['results']:
                    raise Untriggered('acknowledged interrupt and cancellation result were not observed')
                session_id = session_context['sessionId']
                async def observe_interrupt_idle():
                    async for later in client.receive_messages():
                        report['events'] += 1
                        native = serial(later)
                        if isinstance(later, ResultMessage):
                            raise ValueError('unexpected result after cancellation')
                        if not isinstance(native, dict):
                            continue
                        data = native.get('data', native)
                        if (native.get('subtype') == 'session_state_changed' and data.get('state') == 'idle'
                                and data.get('session_id') == session_id and data.get('parent_tool_use_id') is None):
                            return native
                    raise Untriggered('native stream ended before cancellation idle')
                try:
                    idle = await asyncio.wait_for(observe_interrupt_idle(), 5)
                except asyncio.TimeoutError as exc:
                    raise Untriggered('no same-session idle after cancellation result') from exc
                report['interruptClose'] = {'ready': True, 'sessionId': session_id, 'idle': idle}
                transport.interrupt_ready = True
            if index == 0 and parameters.get('requirePostResultEvent'):
                session_id = next((item['result'].get('session_id')
                                   for item in report['receipts'] if 'result' in item), None)
                if not session_id:
                    raise Untriggered('native first result session identity is absent')
                async def observe_idle():
                    async for later in client.receive_messages():
                        report['events'] += 1
                        native = serial(later)
                        data = native.get('data', native)
                        if (native.get('subtype') == 'session_state_changed' and data.get('state') == 'idle'
                                and data.get('session_id') == session_id and data.get('parent_tool_use_id') is None):
                            return native
                    raise Untriggered('native stream ended before a post-result event')
                try:
                    observed = await asyncio.wait_for(observe_idle(), 5)
                except asyncio.TimeoutError as exc:
                    raise Untriggered('no native post-result event observed') from exc
                report['receipts'].append({'post-result-event': observed})
                flow.post_result_event()
            if parameters.get('configurationTiming') == 'during-execution' and not configured_during:
                raise Untriggered('no execution progress barrier for configuration')
            if flow.definition['submission'] == 'progress-barrier':
                # The second input is sent during progress, never replayed as a
                # new sequential request if active input was not observed.
                break
        if flow.definition['submission'] == 'progress-barrier' and report['inputs'] > report['results']:
            async def supplemental_result():
                async for event in client.receive_response():
                    report['events'] += 1
                    if isinstance(event, ResultMessage):
                        report['results'] += 1
                        report['receipts'].append({'result': serial(event)})
                        if event.is_error:
                            raise ValueError('native active-input result is_error')
            await supplemental_result()
        if profile == 'session-close':
            await client.disconnect()
            report['sessionClose'] = {'interactionClosed': True, 'postCloseRejected': False, 'processClosed': False}
            flow.trace.append({'step': 'session-close'})
    if parameters.get('topology'):
        from record_user_story.topology import claude as assess_topology
        assess_topology(report)
    if profile == 'session-close':
        from claude_agent_sdk import CLIConnectionError
        try:
            await client.query('Reply Ready.')
        except CLIConnectionError as exc:
            if str(exc) != 'Not connected. Call connect() first.':
                raise
            report['receipts'].append({'post-close-submit': {'rejected': True, 'type': type(exc).__name__, 'reason': str(exc)}})
            report['sessionClose']['postCloseRejected'] = True
        else:
            raise ValueError('post-close official SDK query unexpectedly accepted')
    if profile == 'history':
        from claude_agent_sdk import list_sessions, get_session_info, get_session_messages
        saved = list_sessions(directory=plan.get('_fixtureProject', plan['project']), limit=10, include_worktrees=False)
        report['receipts'].append({'list_sessions': [serial(x) for x in saved]})
        if not saved:
            raise Untriggered('official SDK found no saved session')
        for item in saved:
            info = get_session_info(item.session_id, directory=plan.get('_fixtureProject', plan['project']))
            report['receipts'].append({'get_session_info': serial(info)})
            if info is None:
                raise Untriggered('discovered session has no readable metadata')
            if info.session_id != item.session_id:
                raise ValueError('history metadata identity differs from discovered session')
            messages = [serial(x) for x in get_session_messages(item.session_id, directory=plan.get('_fixtureProject', plan['project']), limit=100)]
            report['receipts'].append({'get_session_messages': messages})
            if parameters.get('historyTarget') == 'pagination':
                from record_user_story.history import claude_slices
                claude_slices(get_session_messages, item.session_id, plan.get('_fixtureProject', plan['project']), report)
            if parameters.get('historyTarget') in ('children', 'child-messages'):
                from claude_agent_sdk import list_subagents, get_subagent_messages
                agents = list_subagents(item.session_id, directory=plan.get('_fixtureProject', plan['project']))
                report['receipts'].append({'list_subagents': {'sessionId': item.session_id, 'agentIds': agents}})
                if not agents:
                    raise Untriggered('no saved native child discovered independently')
                if parameters['historyTarget'] == 'child-messages':
                    child_messages = [serial(x) for x in get_subagent_messages(item.session_id, agents[0], directory=plan.get('_fixtureProject', plan['project']), limit=100)]
                    report['receipts'].append({'get_subagent_messages': {'sessionId': item.session_id, 'agentId': agents[0], 'messages': child_messages}})
                    if not child_messages:
                        raise Untriggered('saved child has no visible native messages')

            if parameters.get('historyTarget') == 'messages' and not messages:
                raise Untriggered('no saved session messages returned')
