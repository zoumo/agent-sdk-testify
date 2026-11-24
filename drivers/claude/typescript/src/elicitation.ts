import { join } from 'node:path';
import type { ElicitationRequest, ElicitationResult, SDKResultMessage, SDKUserMessage } from '@anthropic-ai/claude-agent-sdk';
import { currentPeer, requireElicitationWire, Untriggered } from './evidence.js';
import { object, type DriverRuntime, type JsonObject } from './runtime.js';
import { sdkPeer } from './sdk.js';

const TOOL = 'mcp__elicitation_fixture__roundtrip';
const DECISIONS = (['form', 'url'] as const).flatMap(mode =>
    (['accept', 'decline', 'cancel'] as const).map(action => ({ mode, action })));

export async function runElicitation(plan: JsonObject, runtime: DriverRuntime, report: JsonObject): Promise<void> {
    const scenario = object(plan.scenario);
    const prompts = scenario.prompts;
    const requests = scenario.requiredRequests;
    if (scenario.driverProfile !== 'standalone-elicitation' || scenario.mode !== 'live-selected-path'
        || !Array.isArray(prompts) || prompts.length !== 1 || typeof prompts[0] !== 'string'
        || !Array.isArray(requests) || requests.length !== 1 || requests[0] !== 'standalone-elicitation') {
        throw new Error('invalid standalone elicitation case projection');
    }
    const prompt = prompts[0];
    const decisions: { request: ElicitationRequest; answer: ElicitationResult }[] = [];
    const results: SDKResultMessage[] = [];
    const flowTrace: JsonObject[] = [];
    const peer = sdkPeer(runtime);
    let events = 0;
    const { query } = await import('@anthropic-ai/claude-agent-sdk');
    let begin!: () => void;
    const ready = new Promise<void>(resolve => { begin = resolve; });

    // The SDK sends the first input only after the peer has finished the
    // initialization exchange. This is also part of the recorded ordering.
    async function* input(): AsyncGenerator<SDKUserMessage> {
        await ready;
        await session.initializationResult();
        flowTrace.push({ step: 'submit', input: 0 });
        yield { type: 'user', session_id: 'default', parent_tool_use_id: null, message: {
            role: 'user', content: [{ type: 'text', text: prompt }],
        } };
    }

    const session = query({ prompt: input(), options: {
        cwd: runtime.workspace,
        pathToClaudeCodeExecutable: runtime.peer.command,
        spawnClaudeCodeProcess: peer.spawn,
        canUseTool: async (name, values) => name === TOOL
            ? { behavior: 'allow', updatedInput: values }
            : { behavior: 'deny', message: 'Unexpected tool in synthetic elicitation case' },
        onElicitation: async request => {
            const expected = DECISIONS[decisions.length];
            if (!expected || request.serverName !== 'elicitation_fixture' || request.mode !== expected.mode
                || request.message !== `SYNTHETIC ${expected.mode} ${expected.action}`) {
                throw new Error('unexpected synthetic elicitation request');
            }
            const answer = decisions.length === 0
                ? { action: expected.action, content: { value: 'SYNTHETIC_FORM' } } : { action: expected.action };
            decisions.push({ request, answer });
            return answer;
        },
    } });

    begin();
    let exit;
    try {
        for await (const message of session) {
            events++;
            if (message.type === 'result') {
                results.push(message);
            }
        }
    } finally {
        try {
            session.close();
        } finally {
            exit = await peer.wait();
            report.peerProcess = { connectionId: 'main', exitCode: exit.exitCode, signal: exit.signal };
            report.inputs = flowTrace.length;
            report.events = events;
            report.requests = decisions.length;
            report.results = results.length;
            report.receipts = [
                ...decisions.map(elicitation => ({ elicitation })),
                ...results.map(result => ({ result })),
            ];
            report.flowTrace = flowTrace;
        }
    }
    if (exit.error) {
        throw exit.error;
    }
    if (exit.exitCode !== 0 || exit.signal !== null) {
        throw new Error('public agent peer exited unsuccessfully');
    }
    const result = results[0];
    if (decisions.length !== 6 || results.length !== 1 || result.subtype !== 'success' || result.is_error !== false) {
        throw new Untriggered('standalone Claude elicitation did not complete all six decisions and the root result');
    }
    flowTrace.push(
        { step: 'request', name: 'standalone-elicitation' },
        { step: 'result', input: 0 },
        { step: 'close-drain' },
    );
    const { raw, ...main } = currentPeer(runtime);
    report.peerProcess = { ...object(report.peerProcess), ...main };
    requireElicitationWire(join(raw, 'raw.jsonl'), decisions);
}
