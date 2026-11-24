import { realpathSync } from 'node:fs';
import { resolve } from 'node:path';
import type { CanUseTool, SDKMessage, SDKTaskNotificationMessage, SDKTaskStartedMessage } from '@anthropic-ai/claude-agent-sdk';
import { Untriggered } from './evidence.js';
import { deferred, readWire, recordInputs, runHistoryPeer, type HistoryRun } from './history-session.js';
import type { JsonObject } from './runtime.js';

const ROLE = 'fixture_reader';
const FILE = 'child-fixture.txt';
const TOKEN = 'SYNTHETIC_CHILD_TOKEN';

function objectField(value: unknown): JsonObject {
    return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as JsonObject : {};
}

export function blocks(event: SDKMessage): JsonObject[] {
    return event.type === 'assistant' ? event.message.content.map(objectField) : [];
}

interface ChildTask {
    start: SDKTaskStartedMessage;
    finish: SDKTaskNotificationMessage;
    startAt: number;
    finishAt: number;
    details: number[];
}

export function completedChildren(events: SDKMessage[], root: string): ChildTask[] {
    const tasks: ChildTask[] = [];
    const calls = new Set(events
        .filter(event => event.type === 'assistant' && event.session_id === root && event.parent_tool_use_id == null)
        .flatMap(blocks).filter(block => block.type === 'tool_use' && block.name === 'Agent').map(block => block.id));
    for (const [startAt, start] of events.entries()) {
        if (start.type !== 'system' || start.subtype !== 'task_started' || start.session_id !== root
            || !start.task_id || !start.tool_use_id || !calls.has(start.tool_use_id)) {
            continue;
        }
        const terminals = events.flatMap((message, index) => message.type === 'system'
            && message.subtype === 'task_notification' && message.session_id === root && message.task_id === start.task_id
            && message.tool_use_id === start.tool_use_id && message.run_id === start.run_id ? [{ message, index }] : []);
        if (terminals.some(({ message }) => message.status !== 'completed')) {
            throw new Error('native child task ended unsuccessfully');
        }
        if (terminals.length !== 1 || terminals[0].index <= startAt) {
            continue;
        }
        const { message: finish, index: finishAt } = terminals[0];
        const details = events.flatMap((event, index) => index > startAt && index < finishAt && event.type === 'assistant'
            && event.session_id === root && event.parent_tool_use_id === start.tool_use_id
            && blocks(event).some(block => block.type === 'text' && block.text) ? [index] : []);
        tasks.push({ start, finish, startAt, finishAt, details });
    }
    if (new Set(tasks.map(task => task.start.task_id)).size !== tasks.length
        || new Set(tasks.map(task => task.start.tool_use_id)).size !== tasks.length) {
        throw new Error('native child lifecycle repeats an identity');
    }
    return tasks;
}

export async function runTopology(run: HistoryRun, prompts: string[]): Promise<void> {
    const session = await runHistoryPeer(run, 'main', { prompts, options: { includePartialMessages: true, forwardSubagentText: true } });
    recordInputs(run, session);
    const root = session.results[0].session_id;
    const children = completedChildren(session.events, root).filter(task => task.details.length > 0);
    const overlaps = children.some((a, index) => children.slice(index + 1)
        .some(b => Math.max(a.startAt, b.startAt) < Math.min(a.finishAt, b.finishAt)));
    if (!overlaps) {
        throw new Untriggered('two native overlapping child lifecycles and correlated outputs not established');
    }
    run.report.topology = {
        mode: 'parallel', rootId: root, nativeToolUseIds: children.map(task => task.start.tool_use_id).sort(),
        children: children.map(task => ({ taskId: task.start.task_id, toolUseId: task.start.tool_use_id,
            startAt: task.startAt, endAt: task.finishAt, outputAt: task.details })),
        verified: true,
    };
}

export async function runFixtureChild(run: HistoryRun, prompts: string[], background: boolean, strict: boolean): Promise<void> {
    const taskPrompt = `Use Read exactly once to read only ${FILE} in the inherited working directory. `
        + (strict ? 'Return exactly the text returned by that read.' : `Return exactly ${TOKEN}.`)
        + ' Do not create agents or descendants. Do not use shell, network, search, writes, or any other file or tool.';
    const release = deferred<boolean>();
    let parentToolId: string | undefined;
    let read: { toolUseId: string; nativeAgentId: string; requestId: string } | undefined;
    let root: string | undefined;
    let task: SDKTaskStartedMessage | undefined;
    const { report, receipts, runtime } = run;
    const canUseTool: CanUseTool = async (name, values, context) => {
        report.requests = Number(report.requests) + 1;
        if (name === 'Agent' && !parentToolId && !context.agentID && context.toolUseID
            && values.subagent_type === ROLE && values.prompt === taskPrompt && (values.run_in_background ?? false) === background) {
            parentToolId = context.toolUseID;
            receipts.push({ 'allow-Agent': { toolUseId: parentToolId } });
            return { behavior: 'allow', updatedInput: values };
        }
        // Native Claude can report its physical execution cwd. Replay uses the
        // sealed cwd for comparison and never reopens that original location.
        const path = values.file_path;
        const workspace = runtime.recordedWorkspace ?? runtime.workspace;
        const nativeWorkspace = runtime.replay ? workspace : realpathSync(runtime.workspace);
        const fixture = typeof path === 'string'
            && [workspace, nativeWorkspace].some(dir => resolve(dir, path) === resolve(dir, FILE));
        if (name === 'Read' && parentToolId && !read && context.agentID && context.toolUseID && fixture) {
            read = { toolUseId: context.toolUseID, nativeAgentId: context.agentID, requestId: context.requestId };
            if (strict) {
                const timeout = setTimeout(() => release.resolve(false), 60_000);
                const released = await release.promise;
                clearTimeout(timeout);
                if (!released) {
                    (report.gaps as string[]).push('background Read was not released by a successful root result');
                    return { behavior: 'deny', message: 'The synthetic root did not complete successfully.' };
                }
            }
            receipts.push({ 'allow-child-Read': { toolUseId: read.toolUseId, nativeAgentId: read.nativeAgentId } });
            return { behavior: 'allow', updatedInput: values };
        }
        (report.gaps as string[]).push(`unauthorized or repeated child tool request: ${name}`);
        return { behavior: 'deny', message: 'Only one fixture_reader child and its synthetic fixture Read are authorized.' };
    };
    const session = await runHistoryPeer(run, 'main', {
        prompts, waitAfterResult: background,
        options: {
            includePartialMessages: true, forwardSubagentText: true, canUseTool,
            agents: { [ROLE]: {
                description: `Read the synthetic ${FILE} and return its token.`,
                prompt: taskPrompt + ' Before completing the child task, send the read result in an assistant text message.'
                    + ' Do not provide the result only as a tool-call argument.',
                background, model: 'inherit', ...(strict ? { tools: ['Read'] } : {}),
            } },
        },
        observe(event) {
            if (event.type === 'system' && event.subtype === 'task_started' && parentToolId
                && event.tool_use_id === parentToolId) {
                task = event;
            }
            if (event.type === 'result' && !root && !('parent_tool_use_id' in event && event.parent_tool_use_id)) {
                root = event.session_id;
                if (strict) {
                    if (task?.session_id !== root) {
                        throw new Error('native root result does not match the background child session');
                    }
                    release.resolve(true);
                }
            }
            if (background && event.type === 'system' && event.subtype === 'task_notification' && task
                && event.session_id === task.session_id && event.task_id === task.task_id
                && event.tool_use_id === parentToolId && event.run_id === task.run_id) {
                if (event.status !== 'completed') {
                    throw new Error('native background child ended unsuccessfully');
                }
                if (!root) {
                    throw new Untriggered('native background child completed before the root result');
                }
                receipts.push({ 'post-root-native-task': event });
                return true;
            }
            return false;
        },
        onExit: () => release.resolve(false),
    });
    recordInputs(run, session);
    if ((report.gaps as string[]).length > 0) {
        throw new Error((report.gaps as string[]).join(' | '));
    }
    const child = completedChildren(session.events, session.results[0].session_id)
        .find(candidate => candidate.start.tool_use_id === parentToolId);
    const linkedRead = read && child && read.nativeAgentId === child.start.task_id
        && session.events.slice(child.startAt + 1, child.finishAt).some(event => event.type === 'assistant'
            && event.session_id === root && event.parent_tool_use_id === parentToolId
            && blocks(event).some(block => block.name === 'Read' && block.type === 'tool_use' && block.id === read?.toolUseId));
    if (!child || !read || !linkedRead || !child.details.some(index => blocks(session.events[index])
        .some(block => block.type === 'text' && typeof block.text === 'string' && block.text.includes(TOKEN)))) {
        throw new Untriggered('native child identity, fixture Read, output and successful completion are not established');
    }
    if (strict) {
        const frames = readWire(session.captured.raw);
        const rootAt = frames.findIndex(({ direction, wire }) => direction === 's2c' && wire.type === 'result'
            && wire.session_id === root && wire.is_error === false && wire.parent_tool_use_id == null);
        const allowedAt = frames.findIndex(({ direction, wire }) => direction === 'c2s' && wire.type === 'control_response'
            && objectField(wire.response).request_id === read?.requestId
            && objectField(objectField(wire.response).response).behavior === 'allow');
        const doneAt = frames.findIndex(({ direction, wire }) => direction === 's2c' && wire.uuid === child.finish.uuid);
        if (!(rootAt >= 0 && rootAt < allowedAt && allowedAt < doneAt)) {
            throw new Untriggered('native child Read was not released between the root result and child completion');
        }
        receipts.push({ 'background-read-barrier': { rootAt, allowedAt, doneAt } });
    }
    report.childEvidence = { sessionId: root, parentToolUseId: parentToolId, taskId: child.start.task_id,
        agentIds: [read.nativeAgentId], outputObserved: true, completed: true };
    receipts.push({ 'native-child-lifecycle': { start: child.start, finish: child.finish,
        output: child.details.map(index => session.events[index]) } });
}
