import { createHash } from 'node:crypto';
import { readFileSync, realpathSync } from 'node:fs';
import { join } from 'node:path';
import { isDeepStrictEqual } from 'node:util';

export function readJSON(path) {
    const value = JSON.parse(readFileSync(path, 'utf8'));
    if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('expected a JSON object');
    return value;
}

function packageID(value) {
    return typeof value === 'string' && value !== '.' && value !== '..' && /^[A-Za-z0-9._-]+$/.test(value);
}

function inScope(root, relative) {
    const path = join(root, relative);
    if (realpathSync(path) !== path) throw new Error('public agent reference traverses a link');
    return path;
}

export function currentCapture(runtime) {
    const text = readFileSync(runtime.agentResults.main, 'utf8');
    const command = runtime.replay ? 'mock' : 'record';
    if (!text.endsWith('\n')) throw new Error('public agent notification is incomplete');
    const notices = text.trimEnd().split('\n').map(line => JSON.parse(line));
    const [started, finished] = notices;
    if (notices.length !== 2 || started.schemaVersion !== 3 || finished.schemaVersion !== 3
        || started.status !== 'running' || finished.status !== 'passed'
        || started.connectionId !== 'main' || started.command !== command
        || !Number.isInteger(finished.processId)
        || ['fixtureId', 'materialId', 'executionId', 'connectionId', 'processId', 'command', 'execution', 'result']
            .some(key => started[key] !== finished[key])
        || (runtime.replay && started.executionSHA256 !== finished.executionSHA256)) {
        throw new Error('public agent notifications disagree');
    }
    const { fixtureId, materialId, executionId } = finished;
    if (![fixtureId, materialId, executionId].every(packageID)) throw new Error('invalid public agent identity');

    const fixture = realpathSync(runtime.fixtureDir);
    const control = readJSON(inScope(fixture, 'fixture.json'));
    if (control.schemaVersion !== 7 || control.fixtureId !== fixtureId) throw new Error('public agent fixture identity differs');
    const root = inScope(fixture, join('packages', materialId));
    const execution = join('executions', executionId);
    const declaration = join(execution, 'execution.json');
    const result = join(execution, 'receipts/main.result.json');
    const receipt = join(execution, 'receipts', runtime.replay ? 'main.json' : 'main.transport.json');
    if (finished.execution !== declaration || finished.result !== result || finished.receipt !== receipt) {
        throw new Error('public agent package references differ');
    }
    const declarationBytes = readFileSync(inScope(root, declaration));
    const descriptor = JSON.parse(declarationBytes);
    if (createHash('sha256').update(declarationBytes).digest('hex') !== finished.executionSHA256
        || descriptor.schemaVersion !== 7 || descriptor.fixtureId !== fixtureId
        || descriptor.executionId !== executionId || descriptor.command !== command
        || descriptor.artifact?.id !== materialId || !isDeepStrictEqual(readJSON(inScope(root, result)), finished)
        || readJSON(inScope(root, 'material/material.json')).id !== materialId) {
        throw new Error('public agent notification differs from durable evidence');
    }
    const raw = inScope(root, 'material/raw');
    if (runtime.replay && realpathSync(runtime.raw) !== raw) throw new Error('public mock capture differs from runtime');
    return { raw, receipt: readJSON(inScope(root, receipt)) };
}
