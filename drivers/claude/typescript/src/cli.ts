#!/usr/bin/env node
import { renameSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { parseArgs } from 'node:util';
import { runElicitation } from './elicitation.js';
import { Untriggered } from './evidence.js';
import { runInteraction, supportsInteraction } from './interactions.js';
import { absolutePath, object, readObject, readRuntime, type JsonObject } from './runtime.js';
import { sdkIdentity } from './sdk.js';

async function run(requestPath: string): Promise<void> {
    const request = readObject(requestPath);
    const runtime = readRuntime(absolutePath(request.runtime, 'runtime'));
    const report: JsonObject = { provider: 'claude', caseId: request.case, gaps: [],
        inputs: 0, results: 0, events: 0, requests: 0, receipts: [] };
    let status = 'passed';
    let message = '';
    try {
        const plan = readObject(join(absolutePath(request.materials, 'materials'), 'case.json'));
        if (plan.provider !== 'claude' || plan.caseId !== request.case) {
            throw new Error('request identity differs from supplied materials');
        }
        const scenario = object(plan.scenario);
        Object.assign(report, {
            story: plan.story,
            storyId: plan.storyId,
            case: plan.case,
            cliVersion: plan.cliVersion,
            scenarioSHA256: plan.scenarioSHA256,
        });
        if (scenario.mode !== 'live-selected-path') {
            status = 'unsupported';
            throw new Error(typeof scenario.reason === 'string' && scenario.reason
                ? scenario.reason : 'case binding has no live path');
        }
        report.officialSDK = sdkIdentity();
        if (plan.caseId === 'SDK-CASE-042') {
            await runElicitation(plan, runtime, report);
        } else if (supportsInteraction(plan)) {
            await runInteraction(plan, runtime, report);
        } else {
            status = 'unsupported';
            throw new Error('case is not implemented by this driver');
        }
    } catch (error) {
        if (status === 'passed') {
            status = error instanceof Untriggered ? 'untriggered' : 'failed';
        }
        message = error instanceof Error ? error.message : String(error);
        report.gaps = [message];
    }

    const captureStatus = status === 'failed' ? 'failed' : status === 'unsupported' ? 'not-executed' : 'complete';
    Object.assign(report, { status, branchStatus: status, captureStatus });
    const reportPath = join(runtime.output, runtime.replay ? 'replay-report.json' : 'report.json');
    writeJSON(reportPath, report);
    const resultPath = join(runtime.output, 'result.json');
    writeJSON(resultPath + '.tmp', { status, message, sdk: report.officialSDK });
    renameSync(resultPath + '.tmp', resultPath);
}

function writeJSON(path: string, value: JsonObject): void {
    writeFileSync(path, JSON.stringify(value, null, 2) + '\n', { flag: 'wx', mode: 0o600 });
}

try {
    const { values } = parseArgs({ options: { request: { type: 'string' } } });
    if (!values.request) {
        throw new Error('usage: agent-sdk-testify-claude --request FILE');
    }
    await run(values.request);
} catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
}
