"""Exercise context injection through the official SDK's native request API."""
from pydantic import BaseModel, ConfigDict

from record_user_story.adapters.common import serial
from record_user_story.scenarios import Untriggered


class Acknowledgement(BaseModel):
    model_config = ConfigDict(extra='allow')


def exercise(client, identity, plan, report, flow):
    timing = flow.definition['parameters']['injectionTiming']
    context_value = flow.definition['parameters']['contextValue']
    injected = False

    def inject(active_turn=None):
        nonlocal injected
        requested = {'threadId': identity, 'items': [
            {'type': 'message', 'role': 'user', 'content': [
                {'type': 'input_text', 'text': 'The stored vault code is ' + context_value + '.'}]}]}
        response = client.request('thread/inject_items', requested, response_model=Acknowledgement)
        if serial(response) != {}:
            raise ValueError('thread/inject_items must return an empty object')
        report['receipts'].append({'context-injected': {
            'timing': timing, 'activeTurnId': active_turn, 'requested': requested,
            'response': serial(response)}})
        injected = True

    for index, prompt in enumerate(flow.inputs()):
        if index == 1 and timing == 'idle':
            inject()
            barrier = client.request('thread/loaded/list', {}, response_model=Acknowledgement)
            report['receipts'].append({'injection-barrier': serial(barrier)})
        flow.submit()
        turn = client.turn_start(identity, [{'type': 'text', 'text': prompt}])
        report['inputs'] += 1
        report['receipts'].append({'input-submitted': serial(turn)})
        while True:
            event = client.next_turn_notification(turn.turn.id)
            report['events'] += 1
            native = serial(event.payload)
            if (index == 0 and timing == 'during-execution' and not injected
                    and event.method == 'item/started'
                    and native.get('item', {}).get('type') not in (None, 'userMessage')):
                inject(turn.turn.id)
            if event.method == 'turn/completed' and native['turn']['id'] == turn.turn.id:
                if native['turn']['status'] != 'completed':
                    raise ValueError('context injection native turn did not complete')
                flow.result()
                report['results'] += 1
                report['receipts'].append({'turn/completed': native})
                break
        if index == 0 and timing == 'during-execution' and not injected:
            raise Untriggered('no active native item before context injection')
