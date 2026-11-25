#!/usr/bin/env node

import { spawnSync } from 'node:child_process';
import { readFileSync, renameSync, writeFileSync } from 'node:fs';
import { dirname, isAbsolute, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { currentCapture, readJSON } from './evidence.mjs';
import { probeExtension, requireExtensionWire, supportsExtension } from './extension.mjs';
import { requireWire } from './wire.mjs';

const sdkName = '@earendil-works/pi-coding-agent';
const sdkVersion = '1.6.0';
const cases = new Set(['001', '002', '005', '006', '021', '022', '023', '024', '027',
    '030', '031', '033', '036']);

const image = {
    type: 'image',
    mimeType: 'image/png',
    data: 'iVBORw0KGgoAAAANSUhEUgAAAEAAAABACAIAAAAlC+aJAAAAeklEQVR4nO3PUQkAIBTAwBfFaEY3iiH8OITBAtxm7fN1wwUNaEEDWtCAFjSgBQ1oQQNa0IAWNKAFDWhBA1rQgBY0oAUNaEEDWtCAFjSgBQ1oQQNa0IAWNKAFDWhBA1rQgBY0oAUNaEEDWtCAFjSgBQ1oQQNa0IAWPHYBSp0BSxN310EAAAAASUVORK5CYII=',
};

function runtimeFrom(path) {
    if (typeof path !== 'string' || !isAbsolute(path)) throw new Error('official driver requires an absolute runtime path');
    const runtime = readJSON(path);
    if (runtime.schemaVersion !== 1 || typeof runtime.replay !== 'boolean') {
        throw new Error('invalid official Pi driver runtime');
    }
    for (const key of ['root', 'workspace', 'home', 'config', 'raw', 'tmp', 'output', 'fixtureDir']) {
        if (typeof runtime[key] !== 'string' || !isAbsolute(runtime[key])) {
            throw new Error(`official Pi driver requires absolute ${key}`);
        }
    }
    const peer = runtime.peer;
    const result = runtime.agentResults?.main;
    if (typeof peer?.command !== 'string' || !isAbsolute(peer.command)
        || (peer.args !== undefined && (!Array.isArray(peer.args) || peer.args.some(arg => typeof arg !== 'string' || arg.includes('\0'))))
        || typeof result !== 'string' || !isAbsolute(result)) {
        throw new Error('invalid supervised Pi peer');
    }
    return runtime;
}

function sdkPath() {
    let entry;
    try {
        entry = fileURLToPath(import.meta.resolve(sdkName));
    } catch (error) {
        throw new Error(`install ${sdkName}@${sdkVersion} alongside the Pi driver`, { cause: error });
    }
    const manifest = readJSON(join(dirname(entry), '..', 'package.json'));
    if (manifest.name !== sdkName || manifest.version !== sdkVersion) {
        throw new Error(`expected ${sdkName}@${sdkVersion}`);
    }
    return entry;
}

function probe(plan, runtime, code) {
    const images = code === '002' ? [image] : undefined;
    const script = code === '001' ? 'continuation.mjs' : 'base.mjs';
    const source = fileURLToPath(new URL(script, import.meta.url));
    const completed = spawnSync(process.execPath, [source], {
        stdio: ['pipe', 'pipe', 'inherit'],
        encoding: 'utf8',
        input: JSON.stringify({
            sdk: sdkPath(), cwd: runtime.workspace,
            peer: [runtime.peer.command, ...(runtime.peer.args ?? [])],
            caseId: plan.caseId, prompts: plan.prompts, images,
            initialModel: plan.initialModel, model: plan.model, effort: plan.scenario.parameters?.effort,
            targetEffort: plan.scenario.parameters?.targetEffort, replay: runtime.replay,
            config: runtime.config, history: plan.historyBindings ?? [],
        }),
    });
    if (completed.error) throw completed.error;
    if (completed.status !== 0) throw new Error('official Pi SDK probe failed');
    return { observed: JSON.parse(completed.stdout), images };
}

async function execute(request) {
    if (typeof request.materials !== 'string' || !isAbsolute(request.materials)) {
        throw new Error('official Pi driver requires an absolute materials path');
    }
    const runtime = runtimeFrom(request.runtime);
    const report = { provider: 'pi', caseId: request.case, gaps: [], inputs: 0, results: 0, events: 0, requests: 0, receipts: [] };
    let status = 'failed';
    let message = '';
    try {
        const plan = readJSON(join(request.materials, 'case.json'));
        const code = plan.caseId?.slice(-3);
        if (plan.provider !== 'pi' || plan.caseId !== request.case || plan.scenario?.caseId !== request.case) {
            throw new Error('request identity differs from the selected Pi case');
        }
        Object.assign(report, {
            story: plan.story, storyId: plan.storyId, case: plan.case,
            scenarioSHA256: plan.scenarioSHA256, cliVersion: plan.cliVersion,
        });
        const extension = supportsExtension(code);
        if ((!cases.has(code) && !extension) || plan.scenario.mode !== 'live-selected-path') {
            status = 'unsupported';
            throw new Error(plan.scenario.reason || 'no official Pi driver for this case');
        }
        if (!Array.isArray(plan.prompts) || !plan.prompts.length || plan.prompts.some(prompt => typeof prompt !== 'string')) {
            throw new Error('Pi case requires text prompts selected from the catalog');
        }
        const { observed, images } = extension ? { observed: await probeExtension(plan, runtime, code) } : probe(plan, runtime, code);
        const evidence = currentCapture(runtime);
        const frames = readFileSync(join(evidence.raw, 'raw.jsonl'), 'utf8').trimEnd().split('\n').map(line => JSON.parse(line));
        if (extension) {
            requireExtensionWire(frames, observed, evidence.receipt, runtime.replay);
        } else {
            requireWire(code, frames, observed, images);
        }
        if (code === '031') {
            const receipt = evidence.receipt;
            const complete = runtime.replay
                ? receipt.fullConsumption === true && receipt.clientEOF === true && receipt.nativeLaunched === false
                : receipt.captureStatus === 'complete' && receipt.reaped === true;
            if (!complete || receipt.cleanupComplete !== true || !observed.sessionClose?.interactionClosed
                || !observed.sessionClose?.postCloseRejected) throw new Error('Pi session and peer did not close');
            observed.sessionClose.processClosed = true;
        }
        Object.assign(report, {
            officialSDK: extension ? observed.sdk : { package: sdkName, version: sdkVersion },
            sessionId: observed.sessionId, receipts: observed.turns, inputs: observed.turns.length,
            results: observed.turns.length,
            events: extension ? observed.wires.length : observed.turns.reduce((count, turn) => count + turn.events, 0),
            requests: extension ? observed.requests : observed.requests?.length ?? 0,
            piExtension: observed.facts, windows: observed.windows, sessionClose: observed.sessionClose,
            savedHistory: observed.savedHistory,
            piConfiguration: observed.configuration?.length ? observed.configuration : undefined,
        });
        if (code === '005') report.inputs++;
        status = 'passed';
    } catch (error) {
        message = error.message;
        report.gaps = [message];
    }
    Object.assign(report, { status, branchStatus: status, captureStatus: status === 'passed' ? 'complete' : 'failed' });
    const path = join(runtime.output, runtime.replay ? 'replay-report.json' : 'report.json');
    writeFileSync(path, JSON.stringify(report, null, 2) + '\n', { mode: 0o600, flag: 'wx' });
    const result = join(runtime.output, 'result.json');
    writeFileSync(result + '.tmp', JSON.stringify({ status, message, sdk: report.officialSDK }) + '\n', { mode: 0o600, flag: 'wx' });
    renameSync(result + '.tmp', result);
}

try {
    const [, , flag, request, ...extra] = process.argv;
    if (flag !== '--request' || !request || extra.length) throw new Error('usage: agent-sdk-testify-pi --request FILE');
    await execute(readJSON(request));
} catch (error) {
    process.stderr.write(error.message + '\n');
    process.exitCode = 2;
}
