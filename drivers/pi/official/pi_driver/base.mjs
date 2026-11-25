import { readFileSync, realpathSync } from 'node:fs';
import { basename, dirname, isAbsolute, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

const input = JSON.parse(readFileSync(0, 'utf8'));
const { RpcClient, SessionManager } = await import(pathToFileURL(input.sdk).href);
const client = new RpcClient({
    cwd: input.cwd,
    cliPath: fileURLToPath(new URL('./peer.mjs', import.meta.url)),
    env: { PI_TESTIFY_PEER: JSON.stringify(input.peer) },
});
const { caseId, prompts, images, initialModel, model, effort, targetEffort, replay } = input;
const code = caseId.slice(-3);
const observed = { turns: [], requests: [], configuration: [] };
const modelChange = ['022', '023', '024'].includes(code);
const modelID = state => state.model ? state.model.provider + '/' + state.model.id : '';
const ensure = (ok, detail) => { if (!ok) throw new Error(detail); };

async function changeModel() {
    const slash = model.indexOf('/');
    ensure(slash > 0 && model !== initialModel, 'model change requires distinct explicit provider/model identities');
    let acknowledgement;
    try {
        acknowledgement = await client.setModel(model.slice(0, slash), model.slice(slash + 1));
    } catch (error) {
        if (code !== '024') throw error;
        observed.configuration.push({ requested: model, outcome: 'rejected' });
        return;
    }
    const native = await client.getState();
    ensure(modelID(native) === model, 'Pi did not read back the requested model');
    observed.configuration.push({
        requested: model, outcome: 'accepted', acknowledgedModel: acknowledgement.id, observed: modelID(native),
    });
}

function progressAction(delta) {
    if (!delta) return undefined;
    if (code === '005') return async () => {
        try {
            const accepted = await client.steer(prompts[1]);
            return { outcome: 'accepted', disposition: accepted.disposition ?? accepted };
        } catch {
            return { outcome: 'rejected' };
        }
    };
    if (code === '030') return async () => {
        await client.abort();
        return { outcome: 'accepted' };
    };
    if (code === '024') return changeModel;
    return undefined;
}

async function runTurn(prompt, media) {
    let action;
    let collect;
    const stopped = client.onEvent(event => {
        const textDelta = event.type === 'message_update' && event.assistantMessageEvent?.type === 'text_delta';
        const act = progressAction(textDelta);
        if (act && !action) action = act().then(value => ({ value }), error => ({ error }));
    });
    try {
        collect = await client.promptAndWait(prompt, media);
        const pending = await action;
        if (pending?.error) throw pending.error;
        const terminal = collect.at(-1);
        const message = collect.findLast(
            event => event.type === 'message_end' && event.message?.role === 'assistant',
        )?.message;
        const reply = message?.content?.filter(block => block.type === 'text').map(block => block.text).join('') ?? '';
        ensure(terminal?.type === 'agent_settled', 'Pi did not publish agent_settled');
        if (code === '030') {
            ensure(pending?.value?.outcome === 'accepted' && terminal.aborted === true && message?.stopReason === 'aborted',
                'Pi did not abort an active execution');
        } else {
            ensure(terminal.aborted === false && message?.stopReason === 'stop' && reply.trim(),
                'Pi did not complete a successful assistant message');
        }
        if (['005', '024'].includes(code)) ensure(action, 'Pi did not produce the native progress barrier');
        if (code === '005' && pending.value.outcome === 'accepted') {
            ensure(pending.value.disposition === 'queued', 'Pi accepted steer without the queued disposition');
            ensure(collect.some(event => event.type === 'message_end' && event.message?.role === 'user'
                && event.message.content?.some?.(block => block.text === prompts[1])),
                'Pi did not process the accepted steer');
        }
        if (code === '002') ensure(reply.trim().toLowerCase() === 'blue', 'Pi did not answer the image question');
        if (code === '006') validateTools(collect);
        return { reply, stopReason: message?.stopReason, terminal: terminal.type, events: collect.length,
            disposition: pending?.value, model: message?.model, provider: message?.provider };
    } finally {
        stopped();
    }
}

function validateTools(events) {
    const starts = events.filter(event => event.type === 'tool_execution_start');
    ensure(starts.map(event => event.toolName).join(',') === 'read,write', 'Pi did not call read then write');
    ensure(starts[0].args?.path === 'numbers.txt' && starts[1].args?.path === 'summary.txt'
        && starts[1].args?.content === 'total=18\n', 'Pi did not use the expected file tool arguments');
    for (const start of starts) {
        ensure(events.some(event => event.type === 'tool_execution_end'
            && event.toolCallId === start.toolCallId && event.isError === false), 'Pi did not report successful tool completion');
        observed.requests.push(start.toolName);
    }
    if (!replay) ensure(readFileSync(join(input.cwd, 'summary.txt'), 'utf8') === 'total=18\n', 'Pi did not write the expected file');
}

function requireEntries(page, cursor) {
    ensure(page.entries.length && page.leafId === page.entries.at(-1).id,
        'Pi returned no entries or a mismatched leaf');
    if (cursor) ensure(page.entries[0].parentId === cursor && page.entries.every(entry => entry.id !== cursor),
        'Pi did not return the append window after the native cursor');
    return page.leafId;
}

let final;
try {
    await client.start();
    const attached = await client.getState();
    const initial = await client.getState();
    ensure(initial.sessionId === attached.sessionId, 'Pi changed identity during startup');
    observed.sessionId = initial.sessionId;
    ensure(observed.sessionId, 'Pi session has no native identity');
    if (code === '021' || modelChange) {
        const expected = modelChange ? initialModel : model;
        ensure(expected && modelID(initial) === expected, 'Pi did not observe the explicitly selected startup model');
        observed.configuration.push({ source: 'startup-get_state', observed: modelID(initial) });
    }
    if (code === '027') {
        ensure(initial.thinkingLevel === effort, 'Pi did not select the requested startup effort');
        observed.configuration.push({ source: 'startup-get_state', observed: initial.thinkingLevel });
    }
    if (code === '022') await changeModel();
    let cursor;
    const inputs = code === '005' ? prompts.slice(0, 1) : prompts;
    for (const [index, prompt] of inputs.entries()) {
        if (index === 1 && code === '023') await changeModel();
        if (index === 1 && code === '027') {
            ensure((await client.getState()).thinkingLevel === effort, 'Pi changed effort before the explicit update');
            const available = await client.getAvailableThinkingLevels();
            ensure(available.includes(targetEffort), 'Pi model did not advertise the target effort');
            await client.setThinkingLevel(targetEffort);
            const effective = (await client.getState()).thinkingLevel;
            ensure(effective === targetEffort, 'Pi did not read back the requested effort');
            observed.configuration.push({ requested: targetEffort, outcome: 'accepted', observed: effective });
        }
        const result = await runTurn(prompt, index === 0 && images ? images : undefined);
        if (code === '021' || code === '022' || code === '023') {
            const expected = code === '023' && index === 0 ? initialModel : model;
            ensure(result.provider + '/' + result.model === expected, 'Pi assistant message did not use the expected model');
        }
        observed.turns.push(result);
        if (code === '036') {
            const page = await client.getEntries(cursor);
            cursor = requireEntries(page, cursor);
            observed.windows ??= [];
            observed.windows.push(page.entries.map(entry => entry.id));
        }
    }
    if (code === '036') {
        const empty = await client.getEntries(cursor);
        ensure(empty.leafId === cursor && empty.entries.length === 0, 'Pi cursor did not reach the current end');
        const tree = await client.getTree();
        const messages = await client.getMessages();
        const stats = await client.getSessionStats();
        ensure(tree.tree.length && tree.leafId === cursor && messages.length >= 4
            && stats.sessionId === observed.sessionId && stats.userMessages >= 2,
            'Pi current session views did not agree');
    }
    final = await client.getState();
    ensure(final.sessionId === observed.sessionId && !final.isStreaming, 'Pi session changed or did not return to idle');
} finally {
    await client.stop();
}

if (code === '033') {
    ensure(isAbsolute(final.sessionFile), 'Pi did not identify a native saved session');
    const directory = join(input.config, 'sessions', basename(dirname(final.sessionFile)));
    const binding = input.history.find(item => item.sessionId === observed.sessionId);
    if (replay) ensure(binding && binding.nativeTarget === 'config/sessions/' + basename(directory) + '/' + basename(final.sessionFile),
        'Pi replay history is absent from the material manifest');
    const cwd = binding?.project ?? realpathSync(input.cwd);
    // Discover before using the ID from the preceding live session.
    const inventory = await SessionManager.list(cwd, directory);
    ensure(inventory.length && inventory.some(item => item.id === observed.sessionId), 'Pi did not discover the stored session');
    const file = SessionManager.findById(cwd, observed.sessionId, directory);
    ensure(file && basename(file) === basename(final.sessionFile), 'Pi did not locate the exact stored session identity');
    const saved = SessionManager.open(file, directory);
    const header = saved.getHeader();
    const messages = saved.getEntries().filter(entry => entry.type === 'message').map(entry => entry.message);
    const text = message => typeof message.content === 'string'
        ? message.content : message.content?.filter(block => block.type === 'text').map(block => block.text).join('');
    ensure(header?.id === observed.sessionId && header.version === 3 && header.timestamp
        && messages.some(message => message.role === 'user' && text(message) === prompts[0])
        && messages.some(message => message.role === 'assistant' && text(message).trim() === 'SYNTHETIC_SAVED'),
        'Pi stored metadata or main conversation is incomplete');
    observed.savedHistory = { discovered: inventory.length, sessionId: header.id, entries: saved.getEntries().length };
}
if (code === '031') {
    let rejected = false;
    try { await client.prompt('unexpected post-close input'); } catch { rejected = true; }
    ensure(rejected, 'Pi accepted input after close');
    observed.sessionClose = { interactionClosed: true, postCloseRejected: true };
}
process.stdout.write(JSON.stringify(observed));
