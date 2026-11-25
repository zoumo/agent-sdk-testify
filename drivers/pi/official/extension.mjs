import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { once } from 'node:events';
import { readFileSync } from 'node:fs';
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
const approvedCommand = "printf 'approved controlled command'";

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

async function heldApproval(client, plan, state) {
    const { start } = await client.prompt(plan.prompts[0]);
    const request = await client.wait(frame => frame.type === 'extension_ui_request'
        && frame.method === 'confirm' && frame.title === 'Agent SDK tool approval', start);
    const detail = JSON.parse(request.frame.message);
    ensure(typeof request.frame.id === 'string' && request.frame.id && typeof detail.toolCallId === 'string'
        && detail.tool === 'bash' && detail.detail === approvedCommand, 'Pi held approval does not match the case command');
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
        frame.type === 'tool_execution_end' && frame.toolCallId === detail.toolCallId),
    'Pi tool completed before approval');
    await client.send({ type: 'extension_ui_response', id: request.frame.id, confirmed: true });
    const result = await client.wait(frame => frame.type === 'tool_execution_end'
        && frame.toolCallId === detail.toolCallId, model.index + 1);
    ensure(result.frame.isError === false
        && messagesText(result.frame.result?.content).trim() === 'approved controlled command', 'Pi did not run the approved command');
    const turn = await client.settled(start);
    const final = await client.command('get_state');
    ensure(final.data?.sessionId === state.sessionId && final.data?.model?.provider === provider
        && final.data?.model?.id === modelId && final.data?.thinkingLevel === level, 'Pi final state differs from the observed controls');
    return {
        turns: [turn], requests: 1,
        facts: {
            approvalId: request.frame.id, toolCallId: detail.toolCallId, approved: true,
            thinkingLevel: level, model: plan.model,
        },
    };
}

async function activeInjection(client, plan, state) {
    const fact = plan.scenario.parameters?.contextValue;
    ensure(typeof fact === 'string' && fact.length > 0 && plan.prompts.length === 2, 'Pi injection requires a catalog fact and two prompts');
    const first = await client.prompt(plan.prompts[0]);
    await client.wait(wire => wire.type === 'message_update' && wire.assistantMessageEvent?.type === 'text_delta', first.start);
    const injected = await client.prompt(injectCommand + ' ' + JSON.stringify([{ type: 'text', text: fact }]), 'handled');
    const original = await client.settled(first.start);
    const turnEnd = client.wires.findIndex((wire, index) => index >= first.start && wire.type === 'turn_end');
    const appended = client.wires.findIndex((wire, index) => index >= first.start && contentMatches(wire, fact));
    ensure(injected.index < turnEnd && turnEnd < appended && appended < original.index,
        'Pi did not append the injected context after the active turn and before settlement');
    const second = await client.prompt(plan.prompts[1]);
    const followUp = await client.settled(second.start);
    ensure(followUp.reply.trim() === fact, 'Pi follow-up did not consume the injected catalog fact');
    const final = await client.command('get_state');
    ensure(final.data?.sessionId === state.sessionId, 'Pi switched sessions during injection');
    ensure(client.wires.filter(wire => wire.type === 'agent_start').length === 2
        && client.wires.filter(wire => contentMatches(wire, fact)).length === 1, 'Pi inject triggered unexpected work or appended more than once');
    return {
        turns: [original, followUp], requests: 0,
        facts: { timing: 'during-execution', disposition: 'handled', appendedAfterTurn: true,
            originalReplyContainsFact: original.reply.includes(fact), followUpConsumedFact: true },
    };
}

const workflows = new Map([['012', heldApproval], ['038', activeInjection]]);

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
        if (code === '038') ensure(capabilities.commands.some(command => command.name === injectCommand.slice(1)
            && command.source === 'extension' && command.sourceInfo?.path === path), 'Pi injection extension is not active');
        const observations = await workflows.get(code)(client, plan, state);
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
