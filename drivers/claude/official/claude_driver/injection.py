"""Preserve shouldQuery on messages sent by the official streaming SDK."""
import asyncio

from claude_agent_sdk import ResultMessage

from record_user_story.adapters.common import serial
from record_user_story.scenarios import Untriggered


async def exercise(client, plan, report, flow):
    timing = flow.definition['parameters']['injectionTiming']
    context_value = flow.definition['parameters']['contextValue']
    messages = asyncio.Queue()
    injected = False

    async def receive():
        try:
            async for event in client.receive_messages():
                await messages.put(event)
        except Exception as exc:
            await messages.put(exc)
        finally:
            await messages.put(None)

    async def next_event():
        event = await asyncio.wait_for(messages.get(), 60)
        if isinstance(event, Exception):
            raise event
        if event is None:
            raise RuntimeError('native stream ended before context injection completed')
        report['events'] += 1
        return event

    async def send(text, should_query):
        async def message():
            yield {'type': 'user', 'message': {'role': 'user', 'content': [
                {'type': 'text', 'text': text}]}, 'parent_tool_use_id': None,
                'shouldQuery': should_query}
        await client.query(message())

    async def inject():
        nonlocal injected
        await send('The stored vault code is ' + context_value + '.', False)
        report['receipts'].append({'context-injected': {
            'timing': timing, 'shouldQuery': False, 'written': True,
            'completionObserved': False}})
        injected = True

    receiver = asyncio.create_task(receive())
    try:
        for index, prompt in enumerate(flow.inputs()):
            if index == 1 and timing == 'idle':
                await inject()
                await client._query._send_control_request({'subtype': 'get_settings'})
                report['receipts'].append({'injection-barrier': {
                    'subtype': 'get_settings', 'acknowledged': True}})
            flow.submit()
            await send(prompt, True)
            report['inputs'] += 1
            report['receipts'].append({'input-submitted': {
                'written': True, 'shouldQuery': True, 'completionObserved': False}})
            while True:
                event = await next_event()
                native = serial(event)
                if (index == 0 and timing == 'during-execution' and not injected
                        and type(event).__name__ == 'StreamEvent'
                        and native.get('parent_tool_use_id') is None):
                    await inject()
                if isinstance(event, ResultMessage):
                    if event.is_error:
                        raise ValueError('context injection native result is_error: ' + str(native))
                    if injected and native.get('num_turns') == 0:
                        report['receipts'].append({'injection-result': native})
                        continue
                    flow.result()
                    report['results'] += 1
                    report['receipts'].append({'result': native})
                    break
            if index == 0 and timing == 'during-execution' and not injected:
                raise Untriggered('no active native stream before context injection')
    finally:
        receiver.cancel()
        await asyncio.gather(receiver, return_exceptions=True)
