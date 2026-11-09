"""Native interactions driven exclusively by the selected official Python SDKs."""
import json
from pathlib import Path

from record_user_story.policy import SYSTEM, driver_profile
from record_user_story.scenarios import Untriggered
from record_user_story.stimuli import content


from record_user_story.adapters.common import serial, proxy_command, configuration_acknowledged


def codex(plan, report, flow):
    from openai_codex.client import CodexClient, CodexConfig
    from openai_codex.generated.v2_all import TurnStartParams
    from pydantic import BaseModel, ConfigDict

    class NativeAcknowledgement(BaseModel):
        model_config = ConfigDict(extra='allow')
    profile = driver_profile(plan)
    calls = set()
    parameters = flow.definition.get('parameters', {})

    def answer(method, params):
        report['requests'] += 1
        params = params or {}
        if method == 'item/tool/call':
            name, args = params.get('tool'), params.get('arguments') or {}
            project = Path(plan.get('_fixtureProject', plan['project'])).resolve()
            path = Path(args.get('path', ''))
            if path.is_absolute():
                try:
                    path = path.relative_to(plan['project'])
                except ValueError:
                    return {'contentItems': [{'type': 'inputText', 'text': 'Fixture path is outside the project'}], 'success': False}
            path = (project / path).resolve()
            if not path.is_relative_to(project):
                return {'contentItems': [{'type': 'inputText', 'text': 'Fixture path is outside the project'}], 'success': False}
            if name == 'fixture_read':
                text = path.read_text()
            elif name == 'fixture_write':
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(args['text'])
                text = 'written'
            else:
                return {'contentItems': [{'type': 'inputText', 'text': 'Unknown fixture tool'}], 'success': False}
            flow.request(name)
            calls.add(name)
            return {'contentItems': [{'type': 'inputText', 'text': text}], 'success': True}
        if method == 'item/tool/requestUserInput':
            answers = {}
            if len(params.get('questions', [])) != parameters.get('questionCount', len(params.get('questions', []))):
                flow.issues.append('native question count differs from target')
            for question in params.get('questions', []):
                labels = [x.get('label') for x in question.get('options', [])]
                choice = flow.definition['answers'].get('choice', 'Ready')
                if labels and choice not in labels and not parameters.get('freeText'):
                    choice = labels[0]
                answers[question['id']] = {'answers': [choice]}
            flow.request('question')
            calls.add('question')
            return {'answers': answers}
        if method in ('item/commandExecution/requestApproval', 'item/fileChange/requestApproval'):
            if parameters.get('approvalKind'):
                kind = 'file' if method == 'item/fileChange/requestApproval' else 'command'
                if kind != parameters['approvalKind']:
                    flow.issues.append('unexpected approval kind: ' + kind)
                flow.request('approval_answer')
                if parameters.get('nativeFileTool') == 'apply_patch' and kind != 'file':
                    return {'decision': 'decline'}
                return {'decision': 'accept' if parameters['approvalDecision'] == 'allow' else 'decline'}
            return {'decision': 'accept'}
        raise NotImplementedError('unsupported native request: ' + method)

    config = CodexConfig(launch_args_override=tuple(proxy_command(plan)), cwd=plan.get('_fixtureProject', plan['project']),
                         client_name='official-python-story-recorder', client_title='Protocol recording')
    with CodexClient(config=config, approval_handler=answer) as client:
        report['receipts'].append({'initialize': serial(client.initialize())})
        params = {'cwd': plan['project'], 'sandbox': 'read-only' if profile == 'approval' else 'workspace-write', 'approvalPolicy': 'on-request',
                  'ephemeral': profile != 'history' and not parameters.get('topology'), 'baseInstructions': SYSTEM,
                  'developerInstructions': 'Complete the supplied task in the synthetic project.'}
        if plan['model']:
            params['model'] = plan.get('initialModel') or plan['model']
        if profile == 'fixture-tools':
            params['dynamicTools'] = [
                {'type': 'function', 'name': 'fixture_read', 'description': 'Read synthetic numbers.txt once.',
                 'inputSchema': {'type': 'object', 'properties': {'path': {'const': 'numbers.txt'}}, 'required': ['path'], 'additionalProperties': False}},
                {'type': 'function', 'name': 'fixture_write', 'description': 'Write synthetic summary.txt once.',
                 'inputSchema': {'type': 'object', 'properties': {'path': {'const': 'summary.txt'}, 'text': {'const': 'sum=42\n'}}, 'required': ['path', 'text'], 'additionalProperties': False}}]
        if parameters.get('configurationTiming') == 'creation':
            if parameters.get('setting') == 'model':
                if not plan['model']:
                    raise ValueError('creation model case needs an explicit native model')
                params['model'] = plan['model']
        if parameters.get('configurationTiming') == 'creation' and parameters.get('setting') == 'thinking':
            params['config'] = {'model_reasoning_effort': 'low'}
        if parameters.get('configurationTiming') == 'between-inputs' and parameters.get('setting') == 'effort':
            initial_effort = parameters.get('effort')
            if not isinstance(initial_effort, str) or not initial_effort:
                raise ValueError('effort case requires an explicit initial effort')
            params['config'] = {'model_reasoning_effort': initial_effort}
        # This pinned SDK's generated model omits dynamicTools. Its documented
        # JsonObject overload preserves this explicit native extension.
        thread = client.thread_start(params)
        identity = thread.thread.id
        report['receipts'].append({'thread/start': serial(thread)})
        def configure():
            requested_settings = [{'threadId': identity, 'model': plan['model'] or thread.model}]
            thinking = flow.definition['configuration'].get('thinking')
            if thinking:
                requested_settings.append({'threadId': identity, **thinking})
            for requested in requested_settings:
                try:
                    response = client.request('thread/settings/update', requested, response_model=NativeAcknowledgement)
                    report['receipts'].append({'thread/settings/update': {'requested': requested, 'accepted': True, 'response': serial(response)}})
                    configuration_acknowledged(report, flow, requested, 'before-first-input')
                except Exception as exc:
                    report['receipts'].append({'thread/settings/update': {'requested': requested, 'accepted': False, 'type': type(exc).__name__, 'error': str(exc)}})
                    raise

        def variant_configuration():
            requested = {'threadId': identity}
            setting = parameters.get('setting')
            if setting == 'effort':
                target_effort = parameters.get('targetEffort')
                if not isinstance(target_effort, str) or not target_effort:
                    raise ValueError('effort case requires an explicit target effort')
                key, value = 'effort', target_effort
            else:
                key, value = (('summary', 'none') if setting == 'display' else ('model', plan['model']))
            requested[key] = value
            response = client.request('thread/settings/update', requested, response_model=NativeAcknowledgement)
            configuration_acknowledged(report, flow, requested, parameters['configurationTiming'])
            report['receipts'].append({'variant-configuration': {'timing': parameters['configurationTiming'], 'requested': requested, 'response': serial(response), 'observed': None}})
        if flow.definition['configuration']:
            configure()
        if parameters.get('configurationTiming') == 'before-first-input':
            variant_configuration()
        for index, prompt in enumerate(flow.inputs()):
            if index == 1 and parameters.get('configurationTiming') == 'between-inputs':
                variant_configuration()
            value = {'threadId': identity, 'input': content('codex', prompt, parameters.get('inputKind'))}
            flow.submit()
            turn = client.turn_start(identity, value['input'], params=TurnStartParams.model_validate(value))
            report['inputs'] += 1
            report['receipts'].append({'input-submitted': serial(turn)})
            configured_during = False
            interrupted = False
            supplemental = None
            while True:
                event = client.next_turn_notification(turn.turn.id)
                report['events'] += 1
                item_type = serial(event.payload).get('item', {}).get('type') if event.method == 'item/started' else None
                item_progress = isinstance(item_type, str) and item_type != 'userMessage'
                if item_progress and parameters.get('configurationTiming') == 'during-execution' and not configured_during:
                    variant_configuration()
                    configured_during = True
                action = flow.progress() if item_progress else None
                if action == 'submit-supplement':
                    interrupted = True
                    report['inputs'] += 1
                    try:
                        supplemental = client.turn_start(identity, [{'type': 'text', 'text': plan['prompts'][1]}])
                        flow.action(action, 'accepted')
                        report['receipts'].append({'active-turn/start': {'accepted': True, 'response': serial(supplemental)}})
                    except Exception as exc:
                        # A native active-turn rejection is an expected outcome
                        # of this flow. Transport/provider/internal errors are not.
                        if getattr(exc, 'code', None) not in (-32600, -32602):
                            raise
                        flow.action(action, 'rejected')
                        report['receipts'].append({'active-turn/start': {'accepted': False, 'type': type(exc).__name__, 'error': str(exc)}})
                        report['facts'].append('native supplemental turn/start rejected; no steer or retry substituted')
                if action == 'interrupt':
                    report['receipts'].append({'turn/interrupt': serial(client.turn_interrupt(identity, turn.turn.id))})
                    flow.action(action, 'accepted')
                    interrupted = True
                if event.method == 'turn/completed' and event.payload.turn.id == turn.turn.id:
                    report['results'] += 1
                    flow.result()
                    report['receipts'].append({'turn/completed': serial(event.payload)})
                    if event.payload.turn.status.value == 'failed':
                        raise ValueError('native turn failed')
                    break
            if flow.definition['submission'] == 'progress-barrier':
                if supplemental is not None and supplemental.turn.id != turn.turn.id:
                    report['receipts'].append({'supplemental/completed': serial(client.wait_for_turn_completed(supplemental.turn.id))})
                    report['results'] += 1
                break
        if parameters.get('topology'):
            from record_user_story.topology import codex as assess_topology
            assess_topology(client, identity, report, serial, parameters['topology'])
        if parameters.get('configurationTiming') == 'during-execution' and not configured_during:
            raise Untriggered('no execution progress barrier for configuration')
        if profile == 'history':
            from record_user_story.history import codex as query_history
            query_history(client, identity, plan, report, parameters)
        if profile == 'session-close':
            client.close()
            report['sessionClose'] = {'interactionClosed': True, 'postCloseRejected': False, 'processClosed': False}
            flow.trace.append({'step': 'session-close'})
    if profile == 'session-close':
        from openai_codex.errors import TransportClosedError
        try:
            client.turn_start(identity, 'Reply Ready.')
        except TransportClosedError as exc:
            report['receipts'].append({'post-close-submit': {'rejected': True, 'type': type(exc).__name__, 'reason': str(exc)}})
            report['sessionClose']['postCloseRejected'] = True
        else:
            raise ValueError('post-close official SDK submit unexpectedly accepted')
