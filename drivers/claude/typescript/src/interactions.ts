import { readFileSync, realpathSync } from 'node:fs';
import { isAbsolute, join, resolve } from 'node:path';
import { PassThrough } from 'node:stream';
import { isDeepStrictEqual } from 'node:util';
import type { Options, PermissionMode, PermissionResult, SDKUserMessage } from '@anthropic-ai/claude-agent-sdk';
import { configurationFor } from './configuration.js';
import { currentPeer, readWire, requirePeerClose, Untriggered } from './evidence.js';
import { cancelled, checkApplicationTool, checkHeld, checkInjection, checkInterrupt, checkPermission,
    checkRootFlow, consumes, idle, rejectedSupplement, type PermissionDecision } from './interaction-wire.js';
import { getSettings, sdkPeer } from './sdk.js';
import { object, readObject, type DriverRuntime, type JsonObject } from './runtime.js';
import { fields, ordered, root } from './wire.js';

const BLUE_IMAGE = 'iVBORw0KGgoAAAANSUhEUgAAAEAAAABACAIAAAAlC+aJAAAAeklEQVR4nO3PUQkAIBTAwBfFaEY3iiH8OITBAtxm7fN1wwUNaEEDWtCAFjSgBQ1oQQNa0IAWNKAFDWhBA1rQgBY0oAUNaEEDWtCAFjSgBQ1oQQNa0IAWNKAFDWhBA1rQgBY0oAUNaEEDWtCAFjSgBQ1oQQNa0IAWPHYBSp0BSxN310EAAAAASUVORK5CYII=';
// Stable for record and strict replay of this case's single supplemental input.
const SUPPLEMENT_ID = '078ed5e3-4c42-4f49-89c6-638b9967a005';
const INTERACTION_PROFILES = new Set(['continuation', 'structured-input', 'active-input', 'fixture-tools',
    'approval', 'deferred-approval', 'questions', 'configuration-timing', 'interrupt', 'session-close', 'context-injection']);

export function supportsInteraction(plan: JsonObject): boolean {
    return INTERACTION_PROFILES.has(String(fields(plan.scenario).driverProfile));
}

function deferred<T>() {
    let resolve!: (value: T) => void;
    const promise = new Promise<T>(done => { resolve = done; });
    return { promise, resolve };
}

async function within<T>(promise: Promise<T>, description: string, ms = 5000): Promise<T> {
    let timer: NodeJS.Timeout | undefined;
    try {
        return await Promise.race([promise, new Promise<never>((_, reject) => {
            timer = setTimeout(() => reject(new Untriggered(description)), ms);
        })]);
    } finally {
        clearTimeout(timer);
    }
}

function userMessage(text: string, kind?: unknown, shouldQuery?: boolean): SDKUserMessage {
    const content: Exclude<Extract<SDKUserMessage['message'], { role: 'user' }>['content'], string> = [{ type: 'text', text }];
    if (kind === 'image' || kind === 'mixed') {
        content.push({ type: 'image', source: { type: 'base64', media_type: 'image/png', data: BLUE_IMAGE } });
    }
    if (kind === 'mixed') {
        content.push({ type: 'text', text: 'This final text block belongs to the same mixed input.' });
    }
    return {
        type: 'user', session_id: 'default', parent_tool_use_id: null, message: {
            role: 'user', content: kind || shouldQuery !== undefined ? content : text,
        }, ...(shouldQuery !== undefined && { shouldQuery }),
    };
}

function questionAnswer(values: JsonObject, params: JsonObject, bound: JsonObject): JsonObject {
    const questions = values.questions;
    const choice = bound.choice;
    if (!Array.isArray(questions) || questions.length !== params.questionCount || typeof choice !== 'string' || !choice) {
        throw new Untriggered('native question count or bound answer differs from the case');
    }
    const answers = new Map<string, string>();
    for (const value of questions) {
        const question = object(value);
        const name = question.question;
        const options = Array.isArray(question.options) ? question.options.map(option => fields(option).label) : [];
        if (typeof name !== 'string' || !name || answers.has(name)
            || (questions.length === 1 && params.freeText !== true && name !== bound.question)
            || (params.freeText === true ? options.includes(choice)
                : !options.includes(choice) || !isDeepStrictEqual(options, bound.options))) {
            throw new Untriggered('native questions do not preserve the bound choices or free-text answer');
        }
        answers.set(name, choice);
    }
    return { ...values, answers: Object.fromEntries(answers) };
}

function expectedFixtureInput(plan: JsonObject, runtime: DriverRuntime, tool: string, values: JsonObject, profile: string, decision: unknown) {
    if (tool === 'Bash') {
        return values.command === 'printf SYNTHETIC_COMMAND';
    }
    const file = tool === 'Read' ? 'numbers.txt' : profile === 'fixture-tools'
        ? 'summary.txt' : decision === 'deny' ? 'denied.txt' : 'approved.txt';
    const expectedContent = file === 'summary.txt' ? 'total=18' : file === 'denied.txt' ? 'denied' : 'SYNTHETIC_APPROVED';
    const path = values.file_path;
    const workspace = realpathSync(runtime.workspace);
    const allowedPaths = [join(runtime.workspace, file), join(workspace, file)];
    if (runtime.replay) {
        // Historical tool payloads may use either the execution cwd or the
        // recorded route alias. These names are compared, never opened.
        for (const recorded of [runtime.recordedWorkspace, plan.project, plan._fixtureProject]) {
            if (typeof recorded === 'string' && isAbsolute(recorded)) {
                allowedPaths.push(join(recorded, file));
            }
        }
    }
    return typeof path === 'string' && allowedPaths.includes(isAbsolute(path) ? path : resolve(workspace, path))
        && (tool === 'Read' || values.content === expectedContent || values.content === expectedContent + '\n');
}

export async function runInteraction(plan: JsonObject, runtime: DriverRuntime, report: JsonObject): Promise<void> {
    const scenario = object(plan.scenario);
    const params = fields(scenario.parameters);
    const profile = String(scenario.driverProfile);
    const prompts = scenario.prompts;
    const requiredRequests = scenario.requiredRequests;
    if (!Array.isArray(prompts) || !prompts.length || prompts.some(p => typeof p !== 'string' || !p)
        || !Array.isArray(requiredRequests) || requiredRequests.some(name => typeof name !== 'string')
        || (plan.driverProfile !== undefined && plan.driverProfile !== profile)) {
        throw new Error('invalid interaction case projection');
    }
    const inject = profile === 'context-injection';
    const interrupt = profile === 'interrupt';
    const close = profile === 'session-close';
    if ((inject && (prompts.length !== 2 || typeof params.contextValue !== 'string' || !params.contextValue
        || !['idle', 'during-execution'].includes(String(params.injectionTiming))))
        || (interrupt && (prompts.length !== 1 || plan.stdinEOFAction !== 'interrupt'))
        || (profile === 'active-input' && prompts.length !== 2)) {
        throw new Error('invalid native case timing or input count');
    }
    const trace: JsonObject[] = [];
    const receipts: JsonObject[] = [];
    Object.assign(report, { flowTrace: trace, receipts });
    const configuration = configurationFor(plan, report, trace);
    const sent: JsonObject[] = [];
    const results: JsonObject[] = [];
    const requests: string[] = [];
    const decisions: PermissionDecision[] = [];
    let events = 0;
    let requestCount = 0;
    let callbackError: unknown;
    let held: { previous: PermissionMode; target: PermissionMode; event: JsonObject } | undefined;
    let pendingHeld: { sessionId: unknown; target: PermissionMode; deliver: (value: JsonObject) => void } | undefined;
    let interruptIdle: JsonObject | undefined;
    let continuationIdle: JsonObject | undefined;
    let fixtureContent: string | undefined;
    const firstInit = deferred<JsonObject>();
    const mcpReady = deferred<void>();
    const injectionWritten = deferred<void>();
    const peer = sdkPeer(runtime, runtime.peer, params.applicationTool === true || params.injectionTiming === 'idle' ? line => {
        const frame = fields(JSON.parse(line));
        if (frame.type === 'user' && frame.shouldQuery === false) {
            injectionWritten.resolve();
        }
        const answer = fields(frame.response);
        const result = fields(fields(fields(answer.response).mcp_response).result);
        if (frame.type === 'control_response' && answer.subtype === 'success' && Array.isArray(result.tools)
            && result.tools.some(value => fields(value).name === 'fixture_read')) {
            mcpReady.resolve();
        }
    } : undefined);
    const { createSdkMcpServer, query, tool } = await import('@anthropic-ai/claude-agent-sdk');
    const inputs = new PassThrough({ objectMode: true });

    function request(name: string) {
        requests.push(name);
        trace.push({ step: 'request', name });
    }

    async function holdPermission() {
        const init = await within(firstInit.promise, 'native session and initial permission mode were not observed');
        const previous = init.permissionMode as PermissionMode;
        if (!init.session_id || !['default', 'acceptEdits', 'bypassPermissions', 'plan', 'dontAsk', 'auto'].includes(previous)) {
            throw new Untriggered('native session and initial permission mode were not observed');
        }
        const target = previous === 'plan' ? 'default' : 'plan';
        const status = deferred<JsonObject>();
        pendingHeld = { sessionId: init.session_id, target, deliver: status.resolve };
        try {
            await session.setPermissionMode(target);
            request('held-control');
            receipts.push({ 'held-permission-control': {
                operation: 'set_permission_mode', requested: target, previousMode: previous, acknowledged: true,
            } });
            try {
                const event = await within(status.promise, 'no same-session native event while approval was pending');
                held = { previous, target, event };
                request('held-event');
                receipts.push({ 'held-permission-event': { native: event } });
            } finally {
                await session.setPermissionMode(previous);
                receipts.push({ 'held-permission-restore': { mode: previous, acknowledged: true } });
            }
        } finally {
            pendingHeld = undefined;
        }
    }

    const canUseTool: NonNullable<Options['canUseTool']> = async (name, values, context): Promise<PermissionResult> => {
        requestCount++;
        try {
            if (profile === 'questions' && name === 'AskUserQuestion') {
                request('question');
                const updatedInput = questionAnswer(values, params, fields(scenario.answers));
                decisions.push({ tool: name, toolUseId: context.toolUseID, input: values, behavior: 'allow', updatedInput });
                return { behavior: 'allow', updatedInput };
            }
            const approvalTool = params.approvalKind === 'file' ? 'Write' : 'Bash';
            const approval = profile === 'approval' && name === approvalTool;
            if (approval || requiredRequests.includes(name)) {
                request(approval ? 'approval_answer' : name);
                if (!expectedFixtureInput(plan, runtime, name, values, profile, params.approvalDecision)) {
                    throw new Untriggered('native approval input differs from the synthetic fixture operation');
                }
                if (profile === 'deferred-approval' && decisions.length === 0) {
                    await holdPermission();
                }
                if (approval && params.approvalDecision === 'deny') {
                    decisions.push({ tool: name, toolUseId: context.toolUseID, input: values, behavior: 'deny' });
                    return { behavior: 'deny', message: 'Synthetic denial; do not retry' };
                }
                decisions.push({ tool: name, toolUseId: context.toolUseID, input: values, behavior: 'allow', updatedInput: values });
                return { behavior: 'allow', updatedInput: values };
            }
            return { behavior: 'deny', message: 'Tool is outside the synthetic scenario' };
        } catch (error) {
            callbackError ??= error;
            return { behavior: 'deny', message: 'Synthetic case could not confirm this request' };
        }
    };

    const mcpServers = params.applicationTool === true ? { fixture: createSdkMcpServer({
        name: 'fixture', version: '1.0.0', tools: [
            tool('fixture_read', 'Read only the synthetic numbers.txt file.',
                { path: (await import('zod')).z.literal('numbers.txt') }, async () => {
                    requestCount++;
                    request('fixture_read');
                    fixtureContent = readFileSync(join(runtime.workspace, 'numbers.txt'), 'utf8');
                    return { content: [{ type: 'text', text: fixtureContent }] };
                }),
        ],
    }) } : undefined;
    const session = query({
        prompt: inputs as AsyncIterable<SDKUserMessage>,
        options: {
            cwd: runtime.workspace,
            pathToClaudeCodeExecutable: runtime.peer.command,
            spawnClaudeCodeProcess: peer.spawn,
            includePartialMessages: true,
            canUseTool,
            mcpServers,
        },
    });

    function submit(message: SDKUserMessage) {
        inputs.write(message);
        sent.push(message);
        if (message.shouldQuery !== false) {
            receipts.push({ 'input-submitted': { written: true, shouldQuery: message.shouldQuery, completionObserved: false } });
        }
    }

    function submitInjection() {
        submit(userMessage('The stored vault code is ' + params.contextValue + '.', undefined, false));
        receipts.push({ 'context-injected': {
            timing: params.injectionTiming, shouldQuery: false, written: true, completionObserved: false,
        } });
    }

    async function nextEvent() {
        const next = await session.next();
        if (next.done) {
            return undefined;
        }
        events++;
        const event = object(next.value);
        if (event.type === 'system' && event.subtype === 'init' && root(event)) {
            firstInit.resolve(event);
        }
        if (pendingHeld && event.type === 'system' && event.subtype === 'status' && root(event)
            && event.session_id === pendingHeld.sessionId && event.permissionMode === pendingHeld.target && event.uuid) {
            pendingHeld.deliver(event);
        }
        return event;
    }

    async function waitForIdle(terminal: JsonObject) {
        while (true) {
            const event = await nextEvent();
            if (!event || event.type === 'result') {
                throw new Untriggered('native stream ended or produced another result before the expected idle');
            }
            if (idle(event, terminal.session_id)) {
                return event;
            }
        }
    }

    let graceful = false;
    try {
        await session.initializationResult();
        if (mcpServers) {
            await within(Promise.race([mcpReady.promise, peer.wait().then(() => {
                throw new Error('public agent peer closed before the SDK MCP tools/list response');
            })]), 'native SDK MCP tools/list was not answered before first input');
            receipts.push({ 'application-MCP-ready': 'native tools/list response written before first input' });
        }
        if (configuration?.timing === 'before-first-input') {
            await configuration.apply(session, 0);
        }
        const activeInput = profile === 'active-input';
        let progressObserved = false;
        let injected = false;
        for (const [i, prompt] of (activeInput ? prompts.slice(0, 1) : prompts).entries()) {
            if (i === 1 && inject && params.injectionTiming === 'idle') {
                submitInjection();
                injected = true;
                // Ensure the queued user object reaches the SDK before sending the control.
                await within(injectionWritten.promise, 'context-only input did not reach the peer');
                await getSettings(session);
                receipts.push({ 'injection-barrier': { subtype: 'get_settings', acknowledged: true } });
            }
            if (i === 1 && configuration?.timing === 'between-inputs') {
                await configuration.apply(session, i);
            }
            trace.push({ step: 'submit', input: i });
            submit(userMessage(prompt, params.inputKind, inject ? true : undefined));
            while (true) {
                const event = activeInput && results.length === 1
                    ? await within(nextEvent(), 'queued native input did not reach its terminal boundary', 60_000)
                    : await nextEvent();
                if (!event) {
                    throw new Untriggered('native response ended without a root result');
                }
                if (!root(event)) {
                    continue;
                }
                if (activeInput && results.length === 1 && idle(event, results[0].session_id)
                    && (consumes(results[0], SUPPLEMENT_ID) || /\bsample\b/i.test(String(results[0].result ?? '')))) {
                    break;
                }
                if (i === 0 && !progressObserved && event.type === 'stream_event') {
                    progressObserved = true;
                    if (activeInput) {
                        trace.push({ step: 'progress-barrier', action: 'submit-supplement' });
                        submit({ ...userMessage(prompts[1]), uuid: SUPPLEMENT_ID });
                        trace.push({ step: 'submit-supplement', outcome: 'written' });
                    } else if (interrupt) {
                        const init = await within(firstInit.promise, 'interrupt session identity was not observed');
                        trace.push({ step: 'progress-barrier', action: 'interrupt' });
                        await session.interrupt();
                        trace.push({ step: 'interrupt', outcome: 'accepted' });
                        receipts.push({ interrupt: 'acknowledged' }, { 'interrupt-session': init.session_id });
                    } else if (inject && params.injectionTiming === 'during-execution') {
                        submitInjection();
                        injected = true;
                    } else if (configuration?.timing === 'during-execution') {
                        await configuration.apply(session, 0);
                    }
                }
                if (event.type !== 'result') {
                    continue;
                }
                if (inject && event.num_turns === 0) {
                    receipts.push({ 'injection-result': event });
                    continue;
                }
                const supplementRejected = activeInput && results.length === 1 && rejectedSupplement(event, SUPPLEMENT_ID);
                results.push(event);
                trace.push({ step: 'result', input: results.length - 1 });
                receipts.push({ result: event });
                if (interrupt) {
                    const init = await within(firstInit.promise, 'interrupt session identity was not observed');
                    if (!progressObserved || !cancelled(event, init.session_id)) {
                        throw new Untriggered('native execution ended without acknowledged cancellation');
                    }
                } else if (!supplementRejected && (event.is_error !== false || event.subtype !== 'success')) {
                    throw new Error('native execution returned an unsuccessful result');
                }
                if (!(activeInput && results.length === 1 && sent.length === 2)) {
                    break;
                }
            }
            const terminal = results.at(-1)!;
            if (interrupt) {
                interruptIdle = await within(waitForIdle(terminal), 'no same-session idle after cancellation');
                report.interruptClose = { ready: true, sessionId: terminal.session_id, idle: interruptIdle };
            }
            if (i === 0 && params.requirePostResultEvent === true) {
                continuationIdle = await within(waitForIdle(terminal), 'no native post-result event before the next input');
                receipts.push({ 'post-result-event': continuationIdle });
                trace.push({ step: 'post-result-event' });
            }
            if (i === 0 && inject && params.injectionTiming === 'during-execution' && !injected) {
                throw new Untriggered('no active native stream before context injection');
            }
        }

        if (close) {
            // Explicit SDK close owns stdin EOF. Check the same SDK object rejects another submission.
            session.close();
            let rejection: unknown;
            try {
                await session.streamInput((async function* () { yield userMessage('Reply Ready.'); })());
            } catch (error) {
                rejection = error;
            }
            if (!(rejection instanceof Error)) {
                throw new Error('post-close official SDK input was not rejected');
            }
            report.sessionClose = { interactionClosed: true, postCloseRejected: true, processClosed: false };
            receipts.push({ 'post-close-submit': { rejected: true, type: rejection.name, reason: rejection.message } });
            trace.push({ step: 'session-close' });
        }
        inputs.end();
        while (true) {
            const trailing = await nextEvent();
            if (!trailing) {
                break;
            }
            if (trailing.type === 'result') {
                throw new Untriggered('unexpected native result after the final execution');
            }
        }
        graceful = true;
    } finally {
        if (!graceful) {
            // Cancel the recorder while stdin is open. An EOF would invoke
            // case 030's success-only native SIGINT path.
            peer.abort();
            await peer.settled();
        }
        inputs.end();
        session.close();
        const exit = await peer.settled();
        Object.assign(report, { inputs: sent.filter(value => value.shouldQuery !== false).length,
            results: results.length, events, requests: requestCount });
        if (exit) {
            report.peerProcess = { connectionId: 'main', exitCode: exit.exitCode, signal: exit.signal };
        }
        if (graceful && (!exit || exit.error || exit.exitCode !== 0 || exit.signal !== null)) {
            throw exit?.error ?? new Error('public agent peer exited unsuccessfully');
        }
    }

    const { raw, ...main } = currentPeer(runtime);
    report.peerProcess = { ...fields(report.peerProcess), ...main };
    const resolved = readObject(join(raw, '..', 'plan.json'));
    if (resolved.provider !== plan.provider || resolved.caseId !== plan.caseId) {
        throw new Error('sealed native launch plan differs from requested case');
    }
    report.cliVersion = resolved.cliVersion;
    if (callbackError) {
        throw callbackError;
    }
    if (!isDeepStrictEqual(requests, requiredRequests)) {
        throw new Untriggered('native request count or order differs from the case: ' + requests.join(', '));
    }
    const frames = readWire(raw);
    const flow = checkRootFlow(frames, sent, results, profile);
    const terminal = flow.results.at(-1)!;
    if (flow.supplement) {
        report.activeInput = flow.supplement;
    }
    if (inject) {
        report.contextInjection = checkInjection(frames, params, flow);
    }
    if (continuationIdle && !frames.some(f => f.direction === 's2c' && idle(f.wire, flow.sessionId)
        && f.wire.uuid === continuationIdle.uuid && ordered(flow.results[0].index, f.index, flow.inputs[1].index))) {
        throw new Untriggered('native post-result event is outside the first terminal and second input boundary');
    }
    if (params.inputKind === 'image' || params.inputKind === 'mixed') {
        if (String(terminal.wire.result).trim().replace(/\.$/, '').toLowerCase() !== 'blue') {
            throw new Untriggered('native answer does not establish supplied image content: blue');
        }
        receipts.push({ 'media-content-observed': { kind: params.inputKind, expected: 'blue' } });
    }
    for (const decision of decisions) {
        const evidence = checkPermission(frames, decision, terminal);
        receipts.push({ 'tool-decision': { name: decision.tool, toolUseId: decision.toolUseId, behavior: decision.behavior,
            requestId: evidence.request.wire.request_id, result: evidence.result.block } });
        if (held) {
            checkHeld(frames, evidence.request, evidence.answer, flow.sessionId, held.event, held.previous, held.target);
        }
    }
    if (mcpServers) {
        if (fixtureContent === undefined) {
            throw new Untriggered('SDK MCP tool did not produce its fixture response');
        }
        checkApplicationTool(frames, flow.inputs[0], terminal, fixtureContent);
    }
    if (configuration) {
        configuration.assess(frames, resolved.args);
    }
    if (interruptIdle) {
        checkInterrupt(frames, flow, interruptIdle);
        requirePeerClose(runtime, { raw, ...main }, true);
        Object.assign(fields(report.interruptClose), { processClosed: true });
        receipts.push({ 'interrupted-result': { terminalReason: terminal.wire.terminal_reason } });
    }
    if (close) {
        requirePeerClose(runtime, { raw, ...main }, false);
        Object.assign(fields(report.sessionClose), { processClosed: true });
    }
    trace.push({ step: 'close-drain' });
}
