import { isDeepStrictEqual } from 'node:util';
import { Untriggered } from './evidence.js';
import { blocks, control, fields, one, ordered, reply, rootFrames, type Frame, type Frames } from './wire.js';
import type { JsonObject } from './runtime.js';

export interface PermissionDecision {
    tool: string;
    toolUseId: string | undefined;
    input: JsonObject;
    behavior: 'allow' | 'deny';
    updatedInput?: JsonObject;
}

export function idle(wire: JsonObject, sessionId: unknown): boolean {
    return wire.type === 'system' && wire.subtype === 'session_state_changed' && wire.state === 'idle'
        && wire.session_id === sessionId && wire.parent_tool_use_id == null && typeof wire.uuid === 'string';
}

export function cancelled(wire: JsonObject, sessionId: unknown): boolean {
    return !!sessionId && wire.session_id === sessionId && wire.parent_tool_use_id == null
        && wire.is_error === true && wire.subtype === 'error_during_execution'
        && ['aborted_streaming', 'aborted_tools'].includes(String(wire.terminal_reason))
        && wire.api_error_status == null && (wire.queued_turn_count ?? 0) === 0;
}

export function consumes(wire: JsonObject, inputId: unknown): boolean {
    return typeof inputId === 'string' && (wire.user_message_uuid === inputId
        || (Array.isArray(wire.user_message_uuids) && wire.user_message_uuids.includes(inputId)));
}

export function rejectedSupplement(wire: JsonObject, inputId: unknown): boolean {
    return consumes(wire, inputId) && wire.is_error === true
        && ['prompt_too_long', 'stop_hook_prevented', 'hook_stopped'].includes(String(wire.terminal_reason))
        && Array.isArray(wire.errors) && wire.errors.some(error => typeof error === 'string' && error);
}

export function checkRootFlow(frames: Frames, sent: JsonObject[], observed: JsonObject[], profile: string) {
    const inputs = rootFrames(frames, 'c2s', 'user');
    const results = rootFrames(frames, 's2c', 'result').filter(f => profile !== 'context-injection' || f.wire.num_turns !== 0);
    if (inputs.length !== sent.length || results.length !== observed.length || !results.length) {
        throw new Untriggered('native input and terminal result counts differ from the SDK flow');
    }
    const sessionId = results[0].wire.session_id;
    if (typeof sessionId !== 'string' || !sessionId || results.some(({ wire }, i) =>
        wire.session_id !== sessionId || typeof wire.uuid !== 'string' || wire.uuid !== observed[i].uuid
        || wire.subtype !== observed[i].subtype || wire.is_error !== observed[i].is_error)) {
        throw new Untriggered('native terminal results do not belong to one observed SDK session');
    }
    for (const [i, { wire }] of inputs.entries()) {
        if (!['default', sessionId].includes(String(wire.session_id)) || wire.shouldQuery !== sent[i].shouldQuery
            || wire.uuid !== sent[i].uuid
            || !isDeepStrictEqual(wire.message, sent[i].message)) {
            throw new Untriggered('SDK native input differs from the projected input');
        }
    }
    if (profile === 'active-input') {
        if (inputs.length !== 2 || results.length < 1 || results.length > 2
            || !rootFrames(frames, 's2c', 'stream_event').some(f => f.wire.session_id === sessionId
                && ordered(inputs[0].index, f.index, inputs[1].index, results[0].index))) {
            throw new Untriggered('supplemental input lacks its active native progress barrier');
        }
        const supplement = inputs[1].wire.uuid;
        const last = results.at(-1)!.wire;
        const rejected = results.length === 2 && rejectedSupplement(last, supplement);
        if (!rejected && !consumes(last, supplement) && !/\bsample\b/i.test(String(last.result ?? ''))) {
            throw new Untriggered('the root result does not establish handling of the supplemental input');
        }
        return { inputs, results, sessionId, supplement: { nativeInputOrdinal: inputs[1].index + 1,
            outcome: rejected ? 'rejected' : results.length === 2 ? 'separate-execution' : 'same-execution',
            ...(rejected && { terminalReason: last.terminal_reason, errors: last.errors }) } };
    }
    const queries = inputs.filter(f => f.wire.shouldQuery !== false);
    if (queries.length !== results.length || queries.some((input, i) =>
        !ordered(results[i - 1]?.index ?? -1, input.index, results[i].index))) {
        throw new Untriggered('explicit query and native terminal boundaries differ');
    }
    return { inputs, results, sessionId };
}

export function checkInjection(frames: Frames, params: JsonObject, flow: ReturnType<typeof checkRootFlow>) {
    const { inputs, results, sessionId } = flow;
    const injected = one(inputs.filter(f => f.wire.shouldQuery === false), 'context-only input');
    const queries = inputs.filter(f => f.wire.shouldQuery === true);
    const context = String(params.contextValue);
    const timing = params.injectionTiming;
    if (inputs.length !== 3 || queries.length !== 2 || results.length !== 2
        || !isDeepStrictEqual(blocks(injected.wire), [{ type: 'text', text: 'The stored vault code is ' + context + '.' }])
        || frames.some(f => f.direction === 'c2s' && f !== injected && JSON.stringify(f.wire).includes(context))) {
        throw new Untriggered('expected one context-only fact and two explicit root queries');
    }
    const ack = one(rootFrames(frames, 's2c', 'result').filter(f => f.wire.num_turns === 0), 'zero-turn context acknowledgement');
    if (ack.wire.session_id !== sessionId || ack.wire.is_error !== false || ack.wire.subtype !== 'success'
        || ack.wire.result !== '' || ack.wire.duration_api_ms !== 0 || fields(ack.wire.usage).output_tokens !== 0
        || !ordered(injected.index, ack.index, results[1].index)) {
        throw new Untriggered('context acknowledgement lacks zero-turn and zero-output evidence');
    }
    const assistants = rootFrames(frames, 's2c', 'assistant');
    const progress = rootFrames(frames, 's2c', 'stream_event');
    if (timing === 'idle') {
        const barrier = one(control(frames, 'c2s', 'get_settings'), 'idle context barrier');
        const answer = reply(frames, barrier);
        if (answer.response.subtype !== 'success' || !ordered(results[0].index, injected.index, barrier.index,
            answer.index, queries[1].index) || [...assistants, ...progress].some(f =>
                ordered(injected.index, f.index, queries[1].index))) {
            throw new Untriggered('idle context injection lacks a successful no-model barrier before the next query');
        }
    } else if (!progress.some(f => f.wire.session_id === sessionId
        && ordered(queries[0].index, f.index, injected.index, results[0].index))) {
        throw new Untriggered('context injection lacks an active native progress barrier');
    }
    const outputs = queries.map((query, i) => assistants
        .filter(f => f.wire.session_id === sessionId && ordered(query.index, f.index, results[i].index))
        .flatMap(f => blocks(f.wire).filter(b => b.type === 'text').map(b => b.text))
        .join(''));
    if (outputs[1].trim() !== context) {
        throw new Untriggered('subsequent query did not return the exact injected fact');
    }
    return { timing, nativeInputOrdinal: injected.index + 1, explicitQueries: 2, extraTurnObserved: false,
        currentQueryUsedFact: outputs[0].includes(context), subsequentQueryUsedFact: true, outputs };
}

function toolResult(frames: Frames, tool: string, id: unknown, sessionId: unknown, after: number, before: number) {
    const calls = rootFrames(frames, 's2c', 'assistant').filter(f => f.wire.session_id === sessionId
        && blocks(f.wire).some(b => b.type === 'tool_use' && b.name === tool));
    const called = one(calls, tool + ' invocation');
    const call = blocks(called.wire).filter(b => b.type === 'tool_use' && b.name === tool);
    if (call.length !== 1 || call[0].id !== id || called.index >= after) {
        throw new Untriggered('native tool invocation differs from its request');
    }
    const returned = one(rootFrames(frames, 's2c', 'user').filter(f => f.wire.session_id === sessionId
        && blocks(f.wire).some(b => b.type === 'tool_result' && b.tool_use_id === id)), tool + ' result');
    if (!ordered(after, returned.index, before)) {
        throw new Untriggered('native tool result is outside the decision and terminal boundary');
    }
    return { frame: returned, block: blocks(returned.wire).find(b => b.type === 'tool_result' && b.tool_use_id === id)! };
}

export function checkPermission(frames: Frames, decision: PermissionDecision, terminal: Frame) {
    const request = one(control(frames, 's2c', 'can_use_tool').filter(f =>
        fields(f.wire.request).tool_name === decision.tool), decision.tool + ' approval');
    const input = fields(request.wire.request);
    const answer = reply(frames, request);
    const response = fields(answer.response.response);
    if (!decision.toolUseId || input.tool_use_id !== decision.toolUseId
        || !isDeepStrictEqual(input.input, decision.input) || answer.response.subtype !== 'success'
        || response.behavior !== decision.behavior
        || (decision.behavior === 'allow' && !isDeepStrictEqual(response.updatedInput, decision.updatedInput))) {
        throw new Untriggered('native permission request or answer differs from the SDK decision');
    }
    const result = toolResult(frames, decision.tool, decision.toolUseId, terminal.wire.session_id, answer.index, terminal.index);
    const invocation = rootFrames(frames, 's2c', 'assistant').find(f => blocks(f.wire)
        .some(b => b.type === 'tool_use' && b.id === decision.toolUseId));
    if ((invocation?.index ?? Infinity) >= request.index
        || (result.block.is_error === true) !== (decision.behavior === 'deny')
        || frames.some(f => f.direction === 's2c' && f.wire.type === 'control_cancel_request'
            && f.wire.request_id === request.wire.request_id && ordered(request.index, f.index, terminal.index))) {
        throw new Untriggered('native tool outcome or cancellation does not match the approval decision');
    }
    if (decision.tool === 'AskUserQuestion') {
        const answers = fields(decision.updatedInput?.answers);
        const consumed = fields(fields(result.frame.wire.tool_use_result).answers);
        if (!isDeepStrictEqual(consumed, answers)) {
            throw new Untriggered('native question result does not acknowledge each answer');
        }
    }
    if (decision.tool === 'Bash' && decision.behavior === 'allow'
        && result.block.content !== 'SYNTHETIC_COMMAND' && fields(result.frame.wire.tool_use_result).stdout !== 'SYNTHETIC_COMMAND') {
        throw new Untriggered('native command result lacks the synthetic output');
    }
    return { request, answer, result };
}

export function checkHeld(frames: Frames, request: Frame, answer: ReturnType<typeof reply>, sessionId: string,
    held: JsonObject, initial: string, target: string) {
    const init = rootFrames(frames, 's2c', 'system').find(f => f.wire.subtype === 'init'
        && f.wire.session_id === sessionId && f.wire.permissionMode === initial && f.index < request.index);
    const delivered = one(rootFrames(frames, 's2c', 'system').filter(f => f.wire.subtype === 'status'
        && f.wire.session_id === sessionId && f.wire.permissionMode === target && f.wire.uuid === held.uuid),
    'held permission status');
    const updates = control(frames, 'c2s', 'set_permission_mode');
    if (!init || updates.length !== 2 || fields(updates[0].wire.request).mode !== target
        || fields(updates[1].wire.request).mode !== initial) {
        throw new Untriggered('held approval did not preserve the native session and original permission mode');
    }
    const [changed, restored] = updates.map(update => reply(frames, update));
    if ([changed, restored].some(({ response }, i) => response.subtype !== 'success'
        || (fields(response.response).mode != null && fields(response.response).mode !== (i === 0 ? target : initial)))
        || !ordered(request.index, updates[0].index, delivered.index, updates[1].index, restored.index, answer.index)
        || changed.index >= updates[1].index) {
        throw new Untriggered('permission control and matching event were not observed while approval was pending');
    }
}

export function checkApplicationTool(frames: Frames, first: Frame, terminal: Frame, fixtureContent: string) {
    function mcp(method: string) {
        const request = one(control(frames, 's2c', 'mcp_message').filter(f => {
            const body = fields(f.wire.request);
            return body.server_name === 'fixture' && fields(body.message).method === method;
        }), method + ' MCP request');
        const answer = reply(frames, request);
        const message = fields(fields(request.wire.request).message);
        const response = fields(fields(answer.response.response).mcp_response);
        if (answer.response.subtype !== 'success' || response.id !== message.id || response.error || !response.result) {
            throw new Untriggered('application tool lacks a correlated MCP success response');
        }
        return { request, answer, message, result: fields(response.result) };
    }
    const list = mcp('tools/list');
    const call = mcp('tools/call');
    const args = fields(call.message.params);
    if (!ordered(list.answer.index, first.index, call.request.index, call.answer.index, terminal.index)
        || !Array.isArray(list.result.tools) || !list.result.tools.some(t => fields(t).name === 'fixture_read')
        || args.name !== 'fixture_read' || fields(args.arguments).path !== 'numbers.txt'
        || call.result.isError === true
        || !isDeepStrictEqual(call.result.content, [{ type: 'text', text: fixtureContent }])) {
        throw new Untriggered('native application tool negotiation, request and fixture response differ');
    }
    const usages = rootFrames(frames, 's2c', 'assistant').flatMap(f =>
        blocks(f.wire).filter(b => b.type === 'tool_use' && b.name === 'mcp__fixture__fixture_read'));
    const id = usages[0]?.id;
    const requestId = fields(args._meta)['claudecode/toolUseId'];
    if (usages.length !== 1 || !id || (requestId && requestId !== id)) {
        throw new Untriggered('MCP request does not match its native assistant tool invocation');
    }
    const result = toolResult(frames, 'mcp__fixture__fixture_read', id, terminal.wire.session_id, call.answer.index, terminal.index);
    if (result.block.is_error === true || !isDeepStrictEqual(result.block.content, call.result.content)) {
        throw new Untriggered('native application tool result did not consume the fixture response');
    }
}

export function checkInterrupt(frames: Frames, flow: ReturnType<typeof checkRootFlow>, observedIdle: JsonObject) {
    const { inputs, results, sessionId } = flow;
    const terminal = one(results, 'interrupt terminal');
    const request = one(control(frames, 'c2s', 'interrupt'), 'interrupt request');
    const answer = reply(frames, request);
    const idled = one(rootFrames(frames, 's2c', 'system').filter(f =>
        idle(f.wire, sessionId) && f.wire.uuid === observedIdle.uuid), 'observed cancellation idle');
    const init = rootFrames(frames, 's2c', 'system').find(f => f.wire.subtype === 'init'
        && f.wire.session_id === sessionId && f.index < request.index);
    if (!init || answer.response.subtype !== 'success' || !cancelled(terminal.wire, sessionId)
        || !ordered(answer.index, terminal.index, idled.index)
        || !rootFrames(frames, 's2c', 'stream_event').some(f => f.wire.session_id === sessionId
            && ordered(inputs[0].index, f.index, request.index))) {
        throw new Untriggered('interrupt acknowledgement, cancellation and same-session idle are not correlated');
    }
}
