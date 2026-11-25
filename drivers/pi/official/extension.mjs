import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { once } from 'node:events';
import { lstatSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { createInterface } from 'node:readline';
import { isDeepStrictEqual } from 'node:util';

const extensionDirectory = 'pi-extension';
const extensionFilename = 'session.ts';
const extensionIdentity = {
    package: '@earendil-works/pi-coding-agent',
    version: '1.6.0',
    client: 'native-stdio',
    extensionRevision: '2dfc08f88c74d5ed3e8390ffabf710e0a55ff28d',
    extensionSHA256: '298c12cce0fe179295cfb1c01d4ae9fa9283c1525c593c6b4dd8be82ea57683b',
};
const injectCommand = '/agent-sdk-inject';
const approvalTitle = 'Agent SDK tool approval';
const questionPrefix = 'Agent SDK question: ';
const marker = 'sdk-test-marker.txt';
const approvedCommand = "printf 'approved controlled command'";
const approvals = new Map([
    ['008', { tool: 'write', detail: marker, content: 'reject this controlled marker\n', confirmed: false }],
    ['009', { tool: 'write', detail: marker, content: 'approved controlled marker\n', confirmed: true }],
    ['010', { tool: 'bash', detail: approvedCommand, confirmed: true }],
    ['011', { tool: 'bash', detail: "printf 'rejected controlled command'", confirmed: false }],
]);

function ensure(ok, message) {
    if (!ok) throw new Error(message);
}

function messagesText(content) {
    if (!Array.isArray(content)) return '';
    return content.filter(block => block.type === 'text').map(block => block.text).join('');
}

class Client {
    constructor(child, output) {
        this.child = child;
        this.output = output[Symbol.asyncIterator]();
        this.wires = [];
        this.serial = 0;
        child.stdin.on('error', error => { this.writeError = error; });
    }

    async next() {
        const item = await this.output.next();
        if (item.done) return false;
        this.wires.push(JSON.parse(item.value));
        return true;
    }

    async wait(select, start = 0) {
        for (let index = start; ; index++) {
            if (index === this.wires.length && !(await this.next())) {
                throw new Error('Pi stdio closed before the expected frame', { cause: this.writeError });
            }
            const frame = this.wires[index];
            if (select(frame)) return { index, frame };
        }
    }

    async send(wire) {
        await new Promise((resolve, reject) => {
            this.child.stdin.write(JSON.stringify(wire) + '\n', error => error ? reject(error) : resolve());
        });
    }

    async command(type, fields = {}) {
        const id = String(++this.serial);
        const start = this.wires.length;
        await this.send({ type, id, ...fields });
        const { frame, index } = await this.wait(wire => wire.type === 'response' && wire.id === id, start);
        ensure(frame.command === type && frame.success === true, 'Pi did not acknowledge ' + type);
        return { start, index, data: frame.data };
    }

    async prompt(message, disposition = 'started') {
        const receipt = await this.command('prompt', { message });
        ensure(receipt.data?.disposition === disposition, 'Pi prompt disposition differs from the required action');
        return receipt;
    }

    async settled(start) {
        const { frame, index } = await this.wait(wire => wire.type === 'agent_settled', start);
        ensure(frame.aborted === false, 'Pi did not settle successfully');
        const assistant = this.wires.slice(start, index).findLast(wire =>
            wire.type === 'message_end' && wire.message?.role === 'assistant');
        const reply = messagesText(assistant?.message?.content);
        ensure(assistant?.message?.stopReason === 'stop' && reply.trim(), 'Pi did not complete the assistant response');
        return { reply, index };
    }

    async drain() {
        while (!(await this.output.next()).done) {
            // Keep the peer's stdout writable until the supervised command stops.
        }
    }
}

function contentMatches(frame, fact) {
    return frame.type === 'message_end' && frame.message?.role === 'custom'
        && frame.message?.customType === 'agent-sdk-inject' && frame.message?.display === false
        && messagesText(frame.message.content) === fact;
}

async function approvalRequest(client, start, expected) {
    const request = await client.wait(frame => frame.type === 'extension_ui_request'
        && frame.method === 'confirm' && frame.title === approvalTitle, start);
    const detail = JSON.parse(request.frame.message);
    const call = client.wires.slice(start, request.index).find(frame =>
        frame.type === 'tool_execution_start' && frame.toolCallId === detail.toolCallId);
    const inputs = expected.tool === 'write'
        ? { path: expected.detail, content: expected.content }
        : { command: expected.detail };
    ensure(typeof request.frame.id === 'string' && request.frame.id && typeof detail.toolCallId === 'string'
        && detail.tool === expected.tool && detail.detail === expected.detail
        && call?.toolName === expected.tool && isDeepStrictEqual(call.args, inputs),
    'Pi approval does not match the original case tool call');
    return { ...request, toolCallId: detail.toolCallId };
}

async function toolResult(client, start, toolCallId, tool) {
    const result = await client.wait(frame => frame.type === 'tool_execution_end'
        && frame.toolCallId === toolCallId, start);
    ensure(result.frame.toolName === tool, 'Pi tool result does not match the original tool call');
    return result.frame;
}

function singleToolTurn(client, start, end) {
    ensure(client.wires.slice(start, end).filter(frame => frame.type === 'tool_execution_start').length === 1,
        'Pi did not execute exactly the tool requested by the case');
}

function verifyWrite(workspace, expected) {
    const path = join(workspace, marker);
    const file = lstatSync(path, { throwIfNoEntry: false });
    if (expected.confirmed) {
        ensure(file?.isFile() && readFileSync(path, 'utf8') === expected.content,
            'Pi did not write the approved marker content');
    } else {
        ensure(!file, 'Pi created the marker after the write was denied');
    }
}

async function toolApproval(client, plan, _state, runtime, code) {
    const expected = approvals.get(code);
    const { start } = await client.prompt(plan.prompts[0]);
    const request = await approvalRequest(client, start, expected);
    await client.send({ type: 'extension_ui_response', id: request.frame.id, confirmed: expected.confirmed });
    const result = await toolResult(client, request.index + 1, request.toolCallId, expected.tool);
    const output = messagesText(result.result?.content).trim();
    const expectedOutput = expected.tool === 'write' ? 'Successfully wrote to ' + marker : 'approved controlled command';
    ensure(result.isError === !expected.confirmed
        && output === (expected.confirmed ? expectedOutput : 'Agent SDK tool approval denied'),
    'Pi tool outcome differs from the approval decision');
    const turn = await client.settled(start);
    singleToolTurn(client, start, turn.index);
    if (expected.tool === 'write' && !runtime.replay) verifyWrite(runtime.workspace, expected);
    return {
        turns: [turn], requests: 1,
        facts: {
            approvalId: request.frame.id, toolCallId: request.toolCallId, tool: expected.tool,
            approved: expected.confirmed, output,
            ...(expected.tool === 'write' && !runtime.replay ? { markerPresent: expected.confirmed } : {}),
        },
    };
}

async function heldApproval(client, plan, state) {
    const { start } = await client.prompt(plan.prompts[0]);
    const request = await approvalRequest(client, start, approvals.get('010'));
    const level = state.thinkingLevel === 'low' ? 'medium' : 'low';
    const change = await client.command('set_thinking_level', { level });
    const changed = await client.wait(frame => frame.type === 'thinking_level_changed' && frame.level === level, change.start);
    const separator = plan.model?.indexOf('/') ?? -1;
    ensure(separator > 0 && separator < plan.model.length - 1, 'Pi held approval requires an explicit provider/model');
    const provider = plan.model.slice(0, separator), modelId = plan.model.slice(separator + 1);
    const model = await client.command('set_model', { provider, modelId });
    ensure(request.index < changed.index && changed.index < model.index
        && model.data?.provider === provider && model.data?.id === modelId,
    'Pi did not emit an independent event and a separate model acknowledgement while approval was held');
    ensure(!client.wires.slice(request.index, model.index).some(frame =>
        frame.type === 'tool_execution_end' && frame.toolCallId === request.toolCallId),
    'Pi tool completed before approval');
    await client.send({ type: 'extension_ui_response', id: request.frame.id, confirmed: true });
    const result = await toolResult(client, model.index + 1, request.toolCallId, 'bash');
    ensure(result.isError === false
        && messagesText(result.result?.content).trim() === 'approved controlled command', 'Pi did not run the approved command');
    const turn = await client.settled(start);
    singleToolTurn(client, start, turn.index);
    const final = await client.command('get_state');
    ensure(final.data?.sessionId === state.sessionId && final.data?.model?.provider === provider
        && final.data?.model?.id === modelId && final.data?.thinkingLevel === level, 'Pi final state differs from the observed controls');
    return {
        turns: [turn], requests: 1,
        facts: {
            approvalId: request.frame.id, toolCallId: request.toolCallId, approved: true,
            thinkingLevel: level, model: plan.model,
        },
    };
}

async function answerQuestion(client, plan) {
    const { question, options, choice } = plan.scenario.answers;
    const method = plan.scenario.parameters.freeText ? 'input' : 'select';
    const args = method === 'select' ? { question, choices: options } : { question };
    const { start } = await client.prompt(plan.prompts[0]);
    const request = await client.wait(frame => frame.type === 'extension_ui_request'
        && frame.method === method && frame.title?.startsWith(questionPrefix), start);
    const detail = JSON.parse(request.frame.title.slice(questionPrefix.length));
    const call = client.wires.slice(start, request.index).find(frame =>
        frame.type === 'tool_execution_start' && frame.toolCallId === detail.toolCallId);
    ensure(typeof request.frame.id === 'string' && request.frame.id && typeof detail.toolCallId === 'string'
        && detail.question === question && call?.toolName === 'agent_sdk_ask' && isDeepStrictEqual(call.args, args)
        && (method === 'input' || isDeepStrictEqual(request.frame.options, options) && options.includes(choice)),
    'Pi question does not match the original case tool call');
    await client.send({ type: 'extension_ui_response', id: request.frame.id, value: choice });
    const result = await toolResult(client, request.index + 1, detail.toolCallId, 'agent_sdk_ask');
    ensure(result.isError === false && result.result?.details?.sdkInteractionExpired === false
        && messagesText(result.result.content) === choice, 'Pi question tool did not receive the answer');
    const turn = await client.settled(start);
    singleToolTurn(client, start, turn.index);
    return {
        turns: [turn], requests: 1,
        facts: { questionId: request.frame.id, toolCallId: detail.toolCallId, method, question, answer: choice },
    };
}

async function injectContext(client, plan, state, _runtime, code) {
    const fact = plan.scenario.parameters?.contextValue;
    ensure(typeof fact === 'string' && fact.length > 0 && plan.prompts.length === 2, 'Pi injection requires a catalog fact and two prompts');
    const active = code === '038';
    const first = await client.prompt(plan.prompts[0]);
    let original;
    if (active) {
        await client.wait(wire => wire.type === 'message_update' && wire.assistantMessageEvent?.type === 'text_delta', first.start);
    } else {
        original = await client.settled(first.start);
    }
    const injected = await client.prompt(injectCommand + ' ' + JSON.stringify([{ type: 'text', text: fact }]), 'handled');
    if (active) {
        original = await client.settled(first.start);
        const turnEnd = client.wires.findIndex((wire, index) => index >= first.start && wire.type === 'turn_end');
        const appended = client.wires.findIndex((wire, index) => index >= first.start && contentMatches(wire, fact));
        ensure(injected.index < turnEnd && turnEnd < appended && appended < original.index,
            'Pi did not append the injected context after the active turn and before settlement');
    } else {
        const barrier = await client.command('get_state');
        const quiet = client.wires.slice(original.index + 1, barrier.index);
        ensure(barrier.data?.sessionId === state.sessionId && barrier.data?.isStreaming === false
            && quiet.some(wire => contentMatches(wire, fact))
            && !quiet.some(wire => wire.type === 'agent_start' || wire.type === 'message_update' || wire.message?.role === 'assistant'),
        'Pi idle injection did not append context silently before the control barrier');
    }
    const second = await client.prompt(plan.prompts[1]);
    const followUp = await client.settled(second.start);
    ensure(followUp.reply.trim() === fact, 'Pi follow-up did not consume the injected catalog fact');
    const final = await client.command('get_state');
    ensure(final.data?.sessionId === state.sessionId, 'Pi switched sessions during injection');
    ensure(client.wires.filter(wire => wire.type === 'agent_start').length === 2
        && client.wires.filter(wire => contentMatches(wire, fact)).length === 1, 'Pi inject triggered unexpected work or appended more than once');
    return {
        turns: [original, followUp], requests: 0,
        facts: { timing: active ? 'during-execution' : 'idle', disposition: 'handled', appendedAfterTurn: true,
            ...(active ? {} : { idleBarrier: 'get_state' }),
            originalReplyContainsFact: original.reply.includes(fact), followUpConsumedFact: true },
    };
}

const workflows = new Map([
    ...[...approvals.keys()].map(code => [code, toolApproval]),
    ['012', heldApproval],
    ['013', answerQuestion],
    ['014', answerQuestion],
    ['037', injectContext],
    ['038', injectContext],
]);

export function supportsExtension(code) {
    return workflows.has(code);
}

export async function probeExtension(plan, runtime, code) {
    const bytes = readFileSync(new URL('./pi-extension/session.ts', import.meta.url));
    ensure(createHash('sha256').update(bytes).digest('hex') === extensionIdentity.extensionSHA256,
        'installed Pi extension differs from its pinned Go SDK source');
    if (runtime.replay) ensure(isDeepStrictEqual(plan.piExtension, extensionIdentity),
        'Pi replay material does not identify this native extension');
    const path = runtime.replay ? plan.args?.at(-1) : join(dirname(runtime.tmp), extensionDirectory, extensionFilename);
    const child = spawn(runtime.peer.command, runtime.peer.args ?? [], { stdio: ['pipe', 'pipe', 'inherit'] });
    // Install the error listener before any application frame is sent.
    const completion = once(child, 'close').then(([status]) => status, error => error);
    const output = createInterface({ input: child.stdout, crlfDelay: Infinity });
    const client = new Client(child, output);
    try {
        const { data: state } = await client.command('get_state');
        const { data: capabilities } = await client.command('get_commands');
        ensure(state?.sessionId && capabilities?.commands?.some(command => command.name === 'agent-sdk-session-ready'
            && command.source === 'extension' && command.sourceInfo?.path === path),
        'Pi did not report the staged session extension');
        if (code === '037' || code === '038') ensure(capabilities.commands.some(command => command.name === injectCommand.slice(1)
            && command.source === 'extension' && command.sourceInfo?.path === path), 'Pi injection extension is not active');
        const observations = await workflows.get(code)(client, plan, state, runtime, code);
        child.stdin.end();
        while (await client.next()) {
            // Consume final frames before validating them against the sealed capture.
        }
        const status = await completion;
        if (status instanceof Error) throw status;
        ensure(status === 0, 'supervised Pi peer did not exit successfully');
        return { ...observations, sessionId: state.sessionId, wires: client.wires, sdk: extensionIdentity };
    } finally {
        if (!child.stdin.writableEnded) child.stdin.end();
        try {
            await client.drain();
        } catch {
            // The output stream has closed. The original failure is preserved.
        }
        output.close();
        await completion;
    }
}

export function requireExtensionWire(frames, observed, receipt, replay) {
    ensure(isDeepStrictEqual(frames.filter(frame => frame.direction === 's2c').map(frame => frame.wire), observed.wires),
        'native Pi frames differ from the supervised agent evidence');
    ensure(receipt.clientEOF === true && receipt.cleanupComplete === true
        && (replay ? receipt.fullConsumption === true && receipt.nativeLaunched === false
            : receipt.captureStatus === 'complete' && receipt.reaped === true && receipt.nativeExitCode === 0),
    'native Pi extension execution did not close and complete');
}
