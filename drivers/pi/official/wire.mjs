import { isDeepStrictEqual } from 'node:util';

function ensure(ok, message) {
    if (!ok) throw new Error(message);
}

function requireContinuation(frames, sessionId) {
    const steps = [];
    let active;
    for (const { direction, wire } of frames) {
        if (direction === 'c2s' && ['get_state', 'prompt'].includes(wire.type)) {
            ensure(!active, 'Pi submitted another command before the current step settled');
            active = { type: wire.type, id: wire.id };
        } else if (direction === 's2c' && wire.type === 'response' && active) {
            ensure(active.id && wire.id === active.id && wire.command === active.type && wire.success === true,
                'Pi response did not acknowledge the active command');
            if (active.type === 'get_state') {
                steps.push(['get_state', wire.data?.sessionId]);
                active = undefined;
            } else if (wire.data?.disposition === 'started') {
                active.started = true;
            }
        } else if (direction === 's2c' && wire.type === 'agent_settled') {
            ensure(active?.started && wire.aborted === false, 'Pi terminal event has no successful prompt');
            steps.push(['prompt', 'agent_settled']);
            active = undefined;
        }
    }
    ensure(sessionId && !active && isDeepStrictEqual(steps, [
        ['get_state', sessionId], ['prompt', 'agent_settled'], ['prompt', 'agent_settled'], ['get_state', sessionId],
    ]), 'Pi did not settle two prompts in the same session');
}

export function requireWire(code, frames, observed, images) {
    if (code === '001') return requireContinuation(frames, observed.sessionId);
    const pending = new Map();
    const commands = [];
    const steps = [];
    const responses = [];
    for (const { direction, wire } of frames) {
        if (direction === 'c2s') {
            ensure(typeof wire.id === 'string' && wire.id && !pending.has(wire.id),
                'Pi sent a command without a unique outstanding ID');
            pending.set(wire.id, wire);
            commands.push(wire.type);
            steps.push(wire.type);
        } else if (wire.type === 'response') {
            const request = pending.get(wire.id);
            ensure(request && request.type === wire.command, 'Pi response did not correlate with a native command');
            pending.delete(wire.id);
            responses.push([request, wire]);
        } else if (wire.type === 'message_update' && wire.assistantMessageEvent?.type === 'text_delta') {
            steps.push('progress');
        } else if (wire.type === 'agent_settled') {
            steps.push(wire.aborted ? 'aborted' : 'settled');
        }
    }
    ensure(!pending.size && commands[0] === 'get_state' && commands.at(-1) === 'get_state',
        'Pi command sequence did not start and finish with a settled state read');
    for (const [request, response] of responses) {
        if (request.type === 'get_state') ensure(response.success && response.data?.sessionId === observed.sessionId,
            'Pi state acknowledgement differs from the client result');
        if (request.type === 'prompt') ensure(response.success && response.data?.disposition === 'started',
            'Pi input acknowledgement differs from the client result');
    }
    const action = { '005': 'steer', '024': 'set_model', '030': 'abort' }[code];
    if (action) {
        const terminal = code === '030' ? 'aborted' : 'settled';
        ensure(steps.filter(step => step === action).length === 1 && steps.includes(terminal)
            && steps.slice(steps.indexOf('prompt') + 1, steps.indexOf(action)).includes('progress')
            && steps.indexOf(action) < steps.indexOf(terminal), `Pi did not send ${action} during active progress`);
        if (code === '030') ensure(!['clear_queue', 'abort_bash', 'abort_retry'].some(kind => commands.includes(kind)),
            'Pi used an additional interrupt control');
    }
    if (code === '002') {
        const [prompt] = responses.find(([request]) => request.type === 'prompt') ?? [];
        ensure(isDeepStrictEqual(prompt?.images, images), 'Pi did not submit the native image stimulus');
    }
    if (code === '036') {
        const windows = responses.filter(([request]) => request.type === 'get_entries');
        const expected = observed.windows;
        ensure(windows.length === 3 && windows[0][0].since == null
            && windows[1][0].since === expected[0].at(-1) && windows[2][0].since === expected[1].at(-1)
            && windows.every(([, response]) => response.success)
            && isDeepStrictEqual(windows.map(([, response]) => response.data.entries.map(entry => entry.id)), [...expected, []]),
        'Pi native entries cursor did not produce the expected windows');
    }
}
