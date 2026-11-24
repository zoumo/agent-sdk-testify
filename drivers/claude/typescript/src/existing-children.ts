import { createHash } from 'node:crypto';
import { readFileSync, realpathSync, statSync } from 'node:fs';
import { dirname, isAbsolute, join, posix } from 'node:path';
import { isDeepStrictEqual } from 'node:util';
import type { SDKMessage } from '@anthropic-ai/claude-agent-sdk';
import { completedChildren } from './children.js';
import { jsonLines, Untriggered } from './evidence.js';
import { readWire, runHistoryPeer, type HistoryRun } from './history-session.js';
import { object, type JsonObject } from './runtime.js';

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const AGENT = /^[A-Za-z0-9_-]+$/;

interface SavedFamily {
    sessionId: string;
    agentIds: string[];
}

function family(value: JsonObject): SavedFamily {
    const { sessionId, agentIds } = value;
    if (value.provider !== 'claude' || typeof sessionId !== 'string' || !UUID.test(sessionId)
        || !Array.isArray(agentIds) || !agentIds.length || new Set(agentIds).size !== agentIds.length
        || agentIds.some(id => typeof id !== 'string' || !AGENT.test(id) || id === sessionId)) {
        throw new Error('035 requires distinct bound native parent and child identities');
    }
    return { sessionId, agentIds };
}

function readBound(root: string, name: unknown, digest?: unknown): Buffer {
    if (typeof name !== 'string' || name.includes('\\') || name.includes('\0')
        || name.split('/').some(part => !part || part === '.' || part === '..')) {
        throw new Error('invalid frozen 035 file reference');
    }
    const path = join(root, name);
    if (realpathSync(path) !== path || !statSync(path).isFile()) {
        throw new Error('035 file reference escapes its frozen root or is not regular');
    }
    const data = readFileSync(path);
    if (digest !== undefined && hash(data) !== digest) {
        throw new Error('frozen 035 evidence bytes changed');
    }
    return data;
}

function hash(data: Buffer): string {
    return createHash('sha256').update(data).digest('hex');
}

function verifyTranscripts(root: string, selected: SavedFamily, bindings: JsonObject[]) {
    const { sessionId, agentIds } = selected;
    const wanted = new Set([sessionId, ...agentIds]);
    const primary = new Set<string>();
    const targets = new Set<string>();
    for (const binding of bindings) {
        const id = binding.sessionId;
        const target = binding.nativeTarget;
        const project = binding.project;
        if (typeof id !== 'string' || !wanted.has(id) || typeof project !== 'string' || !isAbsolute(project)
            || typeof target !== 'string' || targets.has(target) || typeof binding.sha256 !== 'string') {
            throw new Error('invalid or repeated 035 history binding');
        }
        const dir = `config/projects/${project.replace(/[^A-Za-z0-9]/g, '-')}`;
        const base = id === sessionId ? `${dir}/${id}` : `${dir}/${sessionId}/subagents/agent-${id}`;
        const transcript = target === base + '.jsonl';
        if ((!transcript && (id === sessionId || target !== base + '.meta.json'))
            || (binding.parentSessionId ?? '') !== (id === sessionId ? '' : sessionId)
            || (binding.childSessionId ?? '') !== (id === sessionId ? '' : id)) {
            throw new Error('035 history binding differs from native project or parent-child identity');
        }
        targets.add(target);
        const data = readBound(root, binding.source, binding.sha256);
        if (!transcript) {
            object(JSON.parse(data.toString('utf8')));
            continue;
        }
        if (primary.has(id) || jsonLines(data.toString('utf8')).some(row =>
            (row.sessionId != null && row.sessionId !== sessionId) || (row.cwd && row.cwd !== project)
            || (id !== sessionId && row.agentId != null && row.agentId !== id))) {
            throw new Error('035 saved transcript has repeated or mismatched native identity');
        }
        primary.add(id);
    }
    if (primary.size !== wanted.size) {
        throw new Error('035 history does not bind every parent and child transcript');
    }
}

function objects(value: unknown): JsonObject[] {
    if (!Array.isArray(value)) {
        throw new Error('expected frozen 035 history bindings');
    }
    return value.map(object);
}

function setupEvidence(root: string, plan: JsonObject, replay: boolean) {
    const bytes = readBound(root, 'existing-children/setup.json');
    const setup = object(JSON.parse(bytes.toString('utf8')));
    const selected = family(setup);
    if (setup.schemaVersion !== 1 || selected.agentIds.length !== 1 || typeof setup.rawSHA256 !== 'string'
        || typeof setup.transportSHA256 !== 'string' || (replay && !isDeepStrictEqual(setup, plan.existingChildrenSetup))) {
        throw new Error('035 setup differs from the versioned case material');
    }
    const bindings = objects(setup.historyBindings);
    if (bindings.some(binding => binding.role !== 'seed' || binding.source !== `history/seed/${binding.nativeTarget}`)) {
        throw new Error('035 setup history must be frozen before observation');
    }
    verifyTranscripts(root, selected, bindings);
    readBound(root, 'raw/raw.jsonl', setup.rawSHA256);
    const transport = object(JSON.parse(readBound(root, 'raw/transport.json', setup.transportSHA256).toString('utf8')));
    if (transport.captureStatus !== 'complete' || transport.nativeExitCode !== 0 || transport.error != null
        || ['nativeLaunched', 'clientEOF', 'reaped', 'cleanupComplete'].some(key => transport[key] !== true)) {
        throw new Error('035 setup native process did not finish cleanly');
    }
    return { setup, selected, sha256: hash(bytes), completedAt: timestamp(setup.completedAt) };
}

function timestamp(value: unknown): bigint {
    const match = typeof value === 'string' && value.match(/^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)$/);
    const second = match ? Date.parse(match[1] + match[3]) : NaN;
    if (!match || !Number.isFinite(second)) {
        throw new Error('035 phase timestamp is invalid');
    }
    return BigInt(second) * 1_000_000n + BigInt((match[2] ?? '').padEnd(9, '0'));
}

function legacyBindings(root: string, project: string, selected: SavedFamily, inventory: JsonObject): JsonObject[] {
    const { sessionId: parent, agentIds } = selected;
    return Object.entries(inventory).flatMap(([name, digest]) => {
        const target = name.replace(/^history-seed\//, '');
        if (!target.startsWith('config/projects/')) {
            return [];
        }
        const child = target.match(/\/subagents\/agent-([A-Za-z0-9_-]+)\.(?:jsonl|meta\.json)$/)?.[1];
        if (child ? !agentIds.includes(child) : posix.basename(target) !== parent + '.jsonl') {
            return [];
        }
        const primary = name.replace(/\.meta\.json$/, '.jsonl');
        if (typeof digest !== 'string' || typeof inventory[primary] !== 'string') {
            throw new Error('035 legacy seed omits the digest for its saved transcript or sidecar');
        }
        const rows = jsonLines(readBound(root, primary, inventory[primary]).toString('utf8'));
        const nativeProject = rows.find(row => row.cwd)?.cwd ?? project;
        return [{ sessionId: child ?? parent, nativeTarget: target, source: name, sha256: digest, project: nativeProject,
            parentSessionId: child ? posix.basename(posix.dirname(posix.dirname(target))) : undefined,
            childSessionId: child }];
    });
}

function verifySeed(run: HistoryRun, plan: JsonObject): SavedFamily {
    const { runtime } = run;
    const seed = object(plan.historySeed);
    const selected = family(seed);
    const args = Array.isArray(plan.args) ? plan.args : [];
    const resume = args.lastIndexOf('--resume');
    if (!runtime.seedRoot || seed.sourceProject !== plan.historyProject
        || typeof seed.sourceProject !== 'string' || !isAbsolute(seed.sourceProject)
        || resume < 0 || args[resume + 1] !== selected.sessionId) {
        throw new Error('035 legacy observation requires the frozen seed project and native resume');
    }
    const root = realpathSync(runtime.seedRoot);
    if (seed.materialPlan === undefined && runtime.replay) {
        const core = realpathSync(dirname(runtime.raw));
        const inventory = Object.fromEntries(Object.entries(object(seed.files))
            .map(([name, item]) => [name, object(item).sha256]));
        verifyTranscripts(core, selected, legacyBindings(core, seed.sourceProject, selected, inventory));
        return selected;
    }
    const reference = object(seed.materialPlan);
    const prefix = 'history/seed/';
    if (typeof reference.path !== 'string' || !reference.path.startsWith(prefix)
        || posix.basename(reference.path) !== 'plan.json' || typeof reference.sha256 !== 'string') {
        throw new Error('035 legacy seed does not bind its source plan');
    }
    const path = runtime.replay ? reference.path : reference.path.slice(prefix.length);
    const previous = object(JSON.parse(readBound(root, path, reference.sha256).toString('utf8')));
    const previousScenario = object(previous.scenario);
    if (previous.provider !== 'claude' || (previous.caseId != null && previous.caseId !== 'SDK-CASE-034')
        || (previous._fixtureProject || previous.project) !== seed.sourceProject
        || (previous.driverProfile ?? previousScenario.driverProfile) !== 'history'
        || object(previousScenario.parameters).historyTarget !== 'child-messages') {
        throw new Error('035 seed does not come from its bound Claude child-history case');
    }
    const source = join(root, dirname(path));
    const manifest = object(JSON.parse(readBound(source, 'manifest.json').toString('utf8')));
    const wanted = new Set([selected.sessionId, ...selected.agentIds]);
    const canonical = manifest.materialFormatVersion === 3;
    const inventory = object(canonical ? manifest.files : manifest.savedFiles);
    const originals = canonical ? objects(previous.historyBindings)
        : legacyBindings(source, seed.sourceProject, selected, inventory);
    const bindings = originals.filter(binding => wanted.has(String(binding.sessionId)));
    const current = plan.historyBindings === undefined ? [] : objects(plan.historyBindings);
    for (const binding of bindings) {
        if (typeof binding.source !== 'string' || inventory[binding.source] !== binding.sha256) {
            throw new Error('035 seed history differs from its original inventory');
        }
        const pinned = current.find(item => item.role === 'seed' && item.nativeTarget === binding.nativeTarget);
        if ((canonical || pinned) && (!pinned || pinned.source !== `${posix.dirname(reference.path)}/${binding.source}`
            || ['sessionId', 'parentSessionId', 'childSessionId', 'project', 'sha256']
                .some(key => (pinned[key] ?? '') !== (binding[key] ?? '')))) {
            throw new Error('035 seed history differs from the execution binding');
        }
    }
    verifyTranscripts(source, selected, bindings);
    return selected;
}

function resumeHook(message: SDKMessage, parent: string): boolean {
    return message.type === 'system' && (message.subtype === 'hook_started' || message.subtype === 'hook_response')
        && message.session_id === parent && message.hook_name === 'SessionStart:resume'
        && message.hook_event === 'SessionStart' && Boolean(message.uuid);
}

async function discover(run: HistoryRun, selected: SavedFamily) {
    const agentIds = await run.sdk.listSubagents(selected.sessionId, { dir: run.runtime.workspace });
    if (!isDeepStrictEqual([...agentIds].sort(), [...selected.agentIds].sort())) {
        throw new Untriggered('035 SDK did not discover the exact frozen native child identities');
    }
    return { sessionId: selected.sessionId, agentIds };
}

async function observe(run: HistoryRun, id: 'main' | 'probe', selected: SavedFamily) {
    let hookId: unknown;
    const session = await runHistoryPeer(run, id, {
        prompts: [], options: { resume: selected.sessionId, forwardSubagentText: true },
        async observe(event) {
            if (hookId || !resumeHook(event, selected.sessionId)) {
                return false;
            }
            hookId = 'uuid' in event && event.uuid;
            const observed = await discover(run, selected);
            run.receipts.push({ 'existing-observation': event }, { 'existing-children': observed });
            return true;
        },
    });
    const frames = readWire(session.captured.raw);
    const outgoing = frames.filter(frame => frame.direction === 'c2s');
    if (outgoing.length !== 1 || outgoing[0].wire.type !== 'control_request'
        || object(outgoing[0].wire.request).subtype !== 'initialize') {
        throw new Error('035 observation sent input or a control other than initialize');
    }
    const hooks = frames.filter(({ direction, wire }) => direction === 's2c' && wire.uuid === hookId
        && wire.session_id === selected.sessionId && wire.hook_name === 'SessionStart:resume'
        && wire.hook_event === 'SessionStart');
    if (hooks.length !== 1) {
        throw new Error('035 observed parent requires its unique native resume hook');
    }
    return session.captured;
}

export async function runExistingChildren(run: HistoryRun, plan: JsonObject, parameters: JsonObject): Promise<void> {
    const selfSeed = parameters.selfSeed === true && plan.historySeed == null;
    if (selfSeed !== Boolean(run.runtime.probe)) {
        throw new Error('035 public peer inventory differs from the material history mode');
    }
    if (!selfSeed) {
        const selected = verifySeed(run, plan);
        run.receipts.push({ 'existing-pre-observation': await discover(run, selected) });
        await observe(run, 'main', selected);
        verifySeed(run, plan);
        run.report.observationInputs = 0;
        return;
    }

    const prompt = parameters.setupPrompt;
    if (typeof prompt !== 'string' || !prompt.trim()) {
        throw new Error('035 requires its bound setup input');
    }
    const main = await runHistoryPeer(run, 'main', { prompts: [prompt], sessionId: '', options: { forwardSubagentText: true } });
    const first = setupEvidence(main.captured.capture, plan, run.runtime.replay);
    const { setup, selected } = first;
    const root = main.results[0];
    const child = completedChildren(main.events, root.session_id)
        .find(task => task.start.task_id === selected.agentIds[0] && task.start.run_id
            && task.finishAt < main.events.indexOf(root));
    if (selected.sessionId !== root.session_id || !child) {
        throw new Untriggered('035 setup did not finish the bound native child before its parent result');
    }
    const frames = readWire(main.captured.raw);
    const inputs = frames.filter(({ direction, wire }) => direction === 'c2s' && wire.type === 'user');
    if (inputs.length !== 1 || object(inputs[0].wire.message).content !== prompt
        || !['', 'default', selected.sessionId].includes(String(inputs[0].wire.session_id))) {
        throw new Error('035 setup did not submit exactly its bound input');
    }
    Object.assign(run.report, { setupInputs: 1, setupResults: 1 });
    run.receipts.push({ 'existing-setup': setup }, { 'existing-pre-observation': await discover(run, selected) });
    const probe = await observe(run, 'probe', selected);
    if (probe.executionId !== main.captured.executionId || probe.fixtureId !== main.captured.fixtureId
        || probe.materialId !== main.captured.materialId) {
        throw new Error('035 probe did not use its own completed setup execution');
    }
    const final = setupEvidence(probe.capture, plan, run.runtime.replay);
    if (final.sha256 !== first.sha256) {
        throw new Error('035 setup changed during observation');
    }
    const boundary = object(JSON.parse(readBound(probe.capture, 'existing-children/observation.json').toString('utf8')));
    if (boundary.schemaVersion !== 1 || boundary.sessionId !== selected.sessionId
        || boundary.setupSHA256 !== first.sha256 || timestamp(boundary.startedAt) <= first.completedAt) {
        throw new Error('035 observation does not follow its exact completed setup');
    }
    run.receipts.push({ 'existing-observation-boundary': boundary });
    run.report.observationInputs = 0;
}
