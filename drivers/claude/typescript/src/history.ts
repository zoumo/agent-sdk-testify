import { join } from 'node:path';
import { isDeepStrictEqual } from 'node:util';
import type { SDKAssistantMessage, SDKMessage, SessionMessage } from '@anthropic-ai/claude-agent-sdk';
import { blocks, completedChildren, runFixtureChild, runTopology } from './children.js';
import { Untriggered } from './evidence.js';
import { runExistingChildren } from './existing-children.js';
import { recordInputs, runHistoryPeer, type HistoryRun } from './history-session.js';
import { object, readObject, type DriverRuntime, type JsonObject } from './runtime.js';

const PROFILES = new Map([
    ['SDK-CASE-016', 'children'],
    ['SDK-CASE-018', 'topology'],
    ['SDK-CASE-019', 'background-children'],
    ['SDK-CASE-020', 'history'],
    ['SDK-CASE-029', 'history'],
    ['SDK-CASE-033', 'history'],
    ['SDK-CASE-034', 'history'],
    ['SDK-CASE-035', 'existing-children'],
    ['SDK-CASE-036', 'history'],
]);

export function supportsHistoryCase(caseId: unknown): boolean {
    return typeof caseId === 'string' && PROFILES.has(caseId);
}

export async function runHistoryCase(plan: JsonObject, runtime: DriverRuntime, report: JsonObject): Promise<void> {
    const scenario = object(plan.scenario);
    const profile = PROFILES.get(String(plan.caseId));
    const prompts = scenario.prompts ?? plan.prompts;
    const expectedCount = plan.caseId === 'SDK-CASE-036' ? 2 : profile === 'existing-children' ? 0 : 1;
    if (scenario.mode !== 'live-selected-path' || (scenario.driverProfile ?? plan.driverProfile) !== profile
        || (plan.driverProfile !== undefined && plan.driverProfile !== profile)
        || !Array.isArray(prompts) || prompts.length !== expectedCount
        || prompts.some(prompt => typeof prompt !== 'string' || !prompt.trim())
        || (scenario.prompts !== undefined && plan.prompts !== undefined && !isDeepStrictEqual(scenario.prompts, plan.prompts))) {
        throw new Error('invalid Claude child or history case projection');
    }
    const parameters = object(scenario.parameters ?? {});
    // The in-process history APIs resolve saved projects from CLAUDE_CONFIG_DIR;
    // the SDK process launcher alone cannot scope those reads.
    process.env.CLAUDE_CONFIG_DIR = runtime.config;
    delete process.env.CLAUDE_CODE_PROJECT_DIR_NAME;
    const sdk = await import('@anthropic-ai/claude-agent-sdk');
    const run: HistoryRun = { runtime, sdk, report, receipts: report.receipts as JsonObject[] };
    switch (profile) {
        case 'children':
        case 'background-children':
            await runFixtureChild(run, prompts, profile === 'background-children', parameters.backgroundReadBarrier === true);
            break;
        case 'topology':
            if (parameters.topology !== 'parallel') {
                throw new Error('invalid Claude child topology');
            }
            await runTopology(run, prompts);
            break;
        case 'history':
            await runSavedHistory(run, plan, prompts, parameters);
            break;
        case 'existing-children':
            await runExistingChildren(run, plan, parameters);
            break;
        default:
            throw new Error('unsupported Claude child or history profile');
    }
}

async function runSavedHistory(run: HistoryRun, plan: JsonObject, prompts: string[], parameters: JsonObject): Promise<void> {
    const target = (plan.caseId === 'SDK-CASE-033' ? 'messages'
        : plan.caseId === 'SDK-CASE-036' ? 'pagination' : 'child-messages');
    if (parameters.historyTarget !== target) {
        throw new Error('history target differs from the bound case');
    }
    const session = await runHistoryPeer(run, 'main', {
        prompts, sessionId: '', options: { forwardSubagentText: true, includePartialMessages: true },
    });
    recordInputs(run, session);
    const { sdk, runtime, receipts } = run;
    const sessionId = session.results[0].session_id;
    const options = { dir: runtime.workspace };
    const saved = await sdk.listSessions({ ...options, limit: 10, includeWorktrees: false });
    receipts.push({ 'history-setup-ended': { sessionId, connectionId: 'main' } }, { list_sessions: saved });
    if (!saved.some(item => item.sessionId === sessionId)) {
        throw new Untriggered('official SDK did not independently discover the executed session');
    }
    const info = await sdk.getSessionInfo(sessionId, options);
    if (!info || info.sessionId !== sessionId) {
        throw new Untriggered('discovered history lacks metadata for the executed session');
    }
    const messages = await sdk.getSessionMessages(sessionId, { ...options, limit: 100 });
    requireMessages(messages, sessionId);
    receipts.push({ get_session_info: info }, { get_session_messages: messages });
    if (target === 'pagination') {
        if (messages.length < 2 || messages.length >= 100) {
            throw new Untriggered('saved history cannot prove complete multi-slice coverage');
        }
        for (let offset = 0; offset <= messages.length; offset++) {
            const page = await sdk.getSessionMessages(sessionId, { ...options, offset, limit: 1 });
            receipts.push({ 'history-slice': { sessionId, limit: 1, offset, messages: page } });
            if (!isDeepStrictEqual(page, messages.slice(offset, offset + 1))) {
                throw new Error('official SDK history slice lost, repeated or reordered saved content');
            }
        }
        receipts.push({ 'history-whole': { sessionId, messages } });
    }
    if (target !== 'child-messages') {
        return;
    }

    const agentIds = await sdk.listSubagents(sessionId, options);
    if (!agentIds.length || new Set(agentIds).size !== agentIds.length) {
        throw new Untriggered('official SDK found no distinct saved native children');
    }
    const tasks = completedChildren(session.events, sessionId);
    if (agentIds.some(id => !tasks.some(task => task.start.task_id === id))) {
        throw new Untriggered('saved child identities differ from completed native child tasks');
    }
    receipts.push({ list_subagents: { sessionId, agentIds } });
    for (const agentId of agentIds) {
        const childMessages = await sdk.getSubagentMessages(sessionId, agentId, { ...options, limit: 100 });
        requireMessages(childMessages, sessionId);
        receipts.push({ get_subagent_messages: { sessionId, agentId, messages: childMessages } });
    }

    if (plan.caseId === 'SDK-CASE-020') {
        if (!['preserve', 'suppress'].includes(String(parameters.childDetailPolicy))) {
            throw new Error('invalid child detail delivery policy');
        }
        const detailCount = tasks.filter(task => agentIds.includes(task.start.task_id))
            .reduce((count, task) => count + task.details.length, 0);
        if (!detailCount) {
            throw new Untriggered('native material did not supply correlated child text for the delivery policy');
        }
        // The Go SDK applies the delivery policy. Its preserve/suppress runs
        // consume the same unfiltered native material from this driver.
        receipts.push({ 'child-detail-selection': { requested: parameters.childDetailPolicy, rootId: sessionId,
            childIds: [...agentIds].sort(), nativeDetailCount: detailCount,
            scope: 'native source; Go SDK delivery is checked by SDK simulation' } });
    }
    if (plan.caseId === 'SDK-CASE-029') {
        const material = readObject(join(session.captured.capture, 'plan.json'));
        requireInheritance(run, session.events, sessionId, agentIds, plan.model, material, parameters);
    }
}

function requireMessages(messages: SessionMessage[], sessionId: string): void {
    if (!messages.length) {
        throw new Untriggered('official SDK found no visible saved messages');
    }
    if (messages.some(message => !message.uuid || message.session_id !== sessionId)
        || new Set(messages.map(message => message.uuid)).size !== messages.length) {
        throw new Error('saved messages lack distinct native identities for the selected session');
    }
}

function requireInheritance(run: HistoryRun, events: SDKMessage[], rootId: string, agentIds: string[],
    requested: unknown, material: JsonObject, parameters: JsonObject): void {
    const init = events.find(event => event.type === 'system' && event.subtype === 'init' && event.session_id === rootId);
    const args = Array.isArray(material.args) ? material.args : [];
    const modelAt = args.lastIndexOf('--model');
    if (parameters.childConfiguration !== 'model' || typeof requested !== 'string' || !requested
        || (modelAt !== -1 && args[modelAt + 1] !== requested)) {
        throw new Error('root model request differs from the bound configuration');
    }
    if (!init || !('model' in init) || init.model !== requested) {
        throw new Untriggered('native root did not advertise the configured model');
    }
    const assistants = events.filter((event): event is SDKAssistantMessage => event.type === 'assistant' && event.session_id === rootId);
    const rootAssistants = assistants.filter(event => event.parent_tool_use_id == null);
    const models = new Set(rootAssistants.map(event => event.message.model).filter(Boolean));
    const [rootObservedModel] = models;
    if (models.size !== 1) {
        throw new Untriggered('native root did not establish a single effective model');
    }
    const childObservedModels: Record<string, string> = {};
    for (const { start, details } of completedChildren(events, rootId)) {
        if (!agentIds.includes(start.task_id)) {
            continue;
        }
        const input = rootAssistants.flatMap(blocks).find(block => block.type === 'tool_use'
            && block.name === 'Agent' && block.id === start.tool_use_id)?.input;
        const childModels = new Set(assistants.filter(event => event.parent_tool_use_id === start.tool_use_id)
            .map(event => event.message.model).filter(Boolean));
        if (!input || (object(input).model != null && object(input).model !== 'inherit')
            || !details.length || childModels.size !== 1 || !childModels.has(rootObservedModel)) {
            throw new Untriggered('actual child model inheritance is not established');
        }
        childObservedModels[start.task_id] = rootObservedModel;
    }
    if (Object.keys(childObservedModels).length !== agentIds.length) {
        throw new Untriggered('saved child lacks correlated native model evidence');
    }
    run.receipts.push({ 'child-model-inheritance': { requested, rootId, rootAdvertisedModel: init.model,
        rootObservedModel, childObservedModels, scope: 'model only' } });
}
