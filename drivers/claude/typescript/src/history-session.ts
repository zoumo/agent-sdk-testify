import { createHash } from 'node:crypto';
import { readFileSync, realpathSync } from 'node:fs';
import { join } from 'node:path';
import type { Options, SDKMessage, SDKResultMessage, SDKUserMessage } from '@anthropic-ai/claude-agent-sdk';
import { currentPeerEvidence, jsonLines, Untriggered } from './evidence.js';
import { object, type DriverRuntime, type JsonObject } from './runtime.js';
import { sdkPeer } from './sdk.js';

type Connection = 'main' | 'probe';
type SDK = typeof import('@anthropic-ai/claude-agent-sdk');

export interface HistoryRun {
    runtime: DriverRuntime;
    sdk: SDK;
    receipts: JsonObject[];
    report: JsonObject;
}

export interface SessionOptions {
    prompts: string[];
    sessionId?: string;
    options?: Options;
    observe?: (message: SDKMessage) => boolean | Promise<boolean>;
    waitAfterResult?: boolean;
    onExit?: () => void;
}

export function deferred<T>() {
    let resolve!: (value: T) => void;
    const promise = new Promise<T>(done => { resolve = done; });
    return { promise, resolve };
}

export async function runHistoryPeer(run: HistoryRun, id: Connection, config: SessionOptions) {
    const { runtime, sdk, report, receipts } = run;
    const target = id === 'main' ? runtime.peer : runtime.probe;
    if (!target) {
        throw new Error('035 requires both public agent peers');
    }
    const peer = sdkPeer(runtime, target);
    const events: SDKMessage[] = [];
    const results: SDKResultMessage[] = [];
    const flow: JsonObject[] = [];
    const gates = Array.from({ length: config.prompts.length + 1 }, () => deferred<void>());
    let inputs = 0;
    let settled = false;
    const abortController = new AbortController();
    // Each input waits for initialize or for the previous root result. The
    // final gate keeps stdin open through a background task or a resume hook.
    async function* input(): AsyncGenerator<SDKUserMessage> {
        await gates[0].promise;
        await session.initializationResult();
        for (const [index, prompt] of config.prompts.entries()) {
            inputs++;
            flow.push({ step: 'submit', input: index });
            yield { type: 'user', session_id: config.sessionId ?? 'default', parent_tool_use_id: null,
                message: { role: 'user', content: prompt } };
            await gates[index + 1].promise;
        }
    }

    const session = sdk.query({ prompt: input(), options: {
        ...config.options,
        cwd: runtime.workspace,
        pathToClaudeCodeExecutable: target.command,
        spawnClaudeCodeProcess: peer.spawn,
        abortController,
    } });
    const timer = config.prompts.length === 0 ? setTimeout(() => abortController.abort(), 10_000) : undefined;
    if (config.prompts.length > 0) {
        gates[0].resolve();
    }
    let exit;
    let processEvidence: JsonObject;
    try {
        for await (const event of session) {
            events.push(event);
            if (event.type === 'result' && !('parent_tool_use_id' in event && event.parent_tool_use_id)) {
                if (event.subtype !== 'success' || event.is_error !== false || !event.session_id) {
                    throw new Error('native root result was unsuccessful or had no session identity');
                }
                if (results.length < config.prompts.length) {
                    flow.push({ step: 'result', input: results.length });
                    results.push(event);
                    if (results.length < config.prompts.length) {
                        gates[results.length].resolve();
                    }
                } else if (!settled || !config.waitAfterResult || event.session_id !== results[0].session_id) {
                    throw new Error('unexpected extra native root result');
                }
            }
            const observed = await config.observe?.(event);
            if (results.length === config.prompts.length
                && (observed || (!config.waitAfterResult && config.prompts.length > 0))) {
                settled = true;
                clearTimeout(timer);
                gates.at(-1)!.resolve();
            }
        }
    } catch (error) {
        if (abortController.signal.aborted && config.prompts.length === 0 && !settled) {
            throw new Untriggered('035 did not observe the selected native parent before the resume deadline');
        }
        throw error;
    } finally {
        clearTimeout(timer);
        for (const gate of gates) {
            gate.resolve();
        }
        config.onExit?.();
        try {
            session.close();
        } finally {
            exit = await peer.wait();
            processEvidence = { connectionId: id, sdkClosed: true, exitCode: exit.exitCode, signal: exit.signal };
            receipts.push({ 'peer-process': processEvidence });
            if (id === 'main') {
                report.peerProcess = processEvidence;
            }
            report.events = Number(report.events) + events.length;
        }
    }
    if (exit.error) {
        throw exit.error;
    }
    if (exit.exitCode !== 0 || exit.signal !== null) {
        throw new Error(`public agent ${id} peer exited unsuccessfully`);
    }
    if (!settled || inputs !== config.prompts.length || results.some(result => result.session_id !== results[0].session_id)) {
        throw new Untriggered('native session did not complete the selected input or observation flow');
    }
    const captured = currentPeerEvidence(runtime, id);
    const { fixtureId, executionId, materialId } = captured;
    // The Go peer publishes the protocol receipt. The driver can reference and
    // check it after closing its SDK session without minting a second receipt.
    Object.assign(processEvidence, { fixtureId, executionId, materialId, ...protocolReceipt(runtime, captured) });
    flow.push({ step: 'close-drain' });
    return { captured, events, results, inputs, flow };
}


function protocolReceipt(runtime: DriverRuntime, captured: ReturnType<typeof currentPeerEvidence>) {
    const { executionId, connectionId, packageRoot, finished, raw } = captured;
    const extension = runtime.replay ? '.json' : '.transport.json';
    const receipt = `executions/${executionId}/receipts/${connectionId}${extension}`;
    const path = join(packageRoot, receipt);
    if (finished.receipt !== receipt || realpathSync(path) !== path) {
        throw new Error('public agent protocol receipt differs from its connection');
    }
    const bytes = readFileSync(path);
    const facts = object(JSON.parse(bytes.toString('utf8')));
    const digest = (data: Buffer) => createHash('sha256').update(data).digest('hex');
    if (runtime.replay) {
        const rawBytes = readFileSync(join(raw, 'raw.jsonl'));
        if (facts.status !== 'passed' || facts.fullConsumption !== true || facts.nativeLaunched !== false
            || facts.exitCode !== 0 || facts.recordingSHA256 !== digest(rawBytes)) {
            throw new Error('public agent replay receipt differs from its complete native stream');
        }
    } else if (facts.captureStatus !== 'complete' || facts.nativeExitCode !== 0 || facts.reaped !== true
        || !bytes.equals(readFileSync(join(raw, 'transport.json')))) {
        throw new Error('public agent record receipt differs from its completed native capture');
    }
    if (facts.clientEOF !== true || facts.cleanupComplete !== true || facts.error != null) {
        throw new Error('public agent protocol receipt lacks clean EOF and cleanup');
    }
    return { receipt, receiptSHA256: digest(bytes) };
}

export function readWire(directory: string) {
    return jsonLines(readFileSync(join(directory, 'raw.jsonl'), 'utf8'))
        .map(frame => ({ direction: frame.direction, wire: object(frame.wire) }));
}

export function recordInputs(run: HistoryRun, session: Awaited<ReturnType<typeof runHistoryPeer>>) {
    run.report.inputs = session.inputs;
    run.report.results = session.results.length;
    run.report.flowTrace = session.flow;
    run.receipts.push(...session.results.map(result => ({ result })));
}
