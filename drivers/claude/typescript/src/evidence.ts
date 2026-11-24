import { createHash } from 'node:crypto';
import { existsSync, readFileSync, realpathSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { isDeepStrictEqual } from 'node:util';
import { object, readObject, type DriverRuntime, type JsonObject } from './runtime.js';

export class Untriggered extends Error {}

export function jsonLines(text: string): JsonObject[] {
    return text.trimEnd().split('\n').map(line => object(JSON.parse(line)));
}

function packageID(value: unknown): value is string {
    return typeof value === 'string' && value !== '.' && value !== '..' && /^[a-zA-Z0-9._-]+$/.test(value);
}

export function currentPeer(runtime: DriverRuntime) {
    const { fixtureId, executionId, materialId, connectionId, raw } = currentPeerEvidence(runtime, 'main');
    return { fixtureId, executionId, materialId, connectionId, raw };
}

export function currentPeerEvidence(runtime: DriverRuntime, connectionId: 'main' | 'probe') {
    if (!runtime.fixtureDir || !runtime.agentResults[connectionId]) {
        throw new Error('official driver requires connection results and fixtureDir');
    }
    const text = readFileSync(runtime.agentResults[connectionId], 'utf8');
    if (!text.endsWith('\n')) {
        throw new Error('public agent notification is incomplete');
    }
    const notices = jsonLines(text);
    const [started, finished] = notices;
    const command = runtime.replay ? 'mock' : 'record';
    if (notices.length !== 2 || started.schemaVersion !== 3 || finished.schemaVersion !== 3
        || started.status !== 'running' || finished.status !== 'passed'
        || started.connectionId !== connectionId || started.command !== command
        || !Number.isInteger(finished.processId)
        || ['fixtureId', 'materialId', 'executionId', 'connectionId', 'processId', 'command',
            'execution', 'result'].some(key => started[key] !== finished[key])
        || (runtime.replay && started.executionSHA256 !== finished.executionSHA256)) {
        throw new Error('public agent notifications disagree');
    }
    const { fixtureId, executionId, materialId } = finished;
    const fixture = realpathSync(runtime.fixtureDir);
    const control = readObject(join(fixture, 'fixture.json'));
    if (!packageID(executionId) || !packageID(materialId) || !packageID(fixtureId)
        || control.schemaVersion !== 7 || control.fixtureId !== fixtureId) {
        throw new Error('public agent identity differs from fixture');
    }
    const root = join(fixture, 'packages', materialId);
    const execution = `executions/${executionId}`;
    const declaration = `${execution}/execution.json`;
    const result = `${execution}/receipts/${connectionId}.result.json`;
    if (finished.execution !== declaration || finished.result !== result
        || realpathSync(root) !== root || realpathSync(join(root, declaration)) !== join(root, declaration)
        || realpathSync(join(root, result)) !== join(root, result)) {
        throw new Error('public agent references differ from its package');
    }
    const declarationBytes = readFileSync(join(root, declaration));
    const descriptor = object(JSON.parse(declarationBytes.toString('utf8')));
    if (createHash('sha256').update(declarationBytes).digest('hex') !== finished.executionSHA256
        || descriptor.schemaVersion !== 7 || descriptor.fixtureId !== fixtureId
        || descriptor.executionId !== executionId || descriptor.command !== command
        || object(descriptor.artifact).id !== materialId
        || !isDeepStrictEqual(readObject(join(root, result)), finished)) {
        throw new Error('public agent notification differs from durable evidence');
    }
    const material = join(root, 'material', 'material.json');
    const published = existsSync(material);
    if ((runtime.replay || published) && readObject(material).id !== materialId) {
        throw new Error('public agent material differs from runtime');
    }
    // In 035 record, main finishes with its settled receipt before probe is
    // allowed to start. Only the last connection publishes the material.
    const capture = runtime.replay || published ? dirname(material) : join(root, execution, 'capture');
    if (realpathSync(capture) !== capture || (runtime.replay && realpathSync(runtime.raw) !== join(capture, 'raw'))) {
        throw new Error('public agent capture differs from runtime');
    }
    const raw = connectionId === 'main' ? join(capture, 'raw') : join(capture, 'remote-probe', 'raw');
    return { fixtureId, executionId, materialId, connectionId, capture, raw, packageRoot: root, finished };
}

interface ElicitationReceipt {
    request: { mode?: string; message: string };
    answer: unknown;
}

function field(value: unknown, name: string): unknown {
    if (typeof value === 'object' && value !== null) {
        return (value as JsonObject)[name];
    }
    return undefined;
}

export function requireElicitationWire(path: string, decisions: ElicitationReceipt[]): void {
    let text: string;
    try {
        text = readFileSync(path, 'utf8');
    } catch (error) {
        if ((error as NodeJS.ErrnoException).code === 'ENOENT') {
            throw new Untriggered('native elicitation capture is absent');
        }
        throw error;
    }
    if (!text.trim()) {
        throw new Untriggered('native elicitation capture is empty');
    }
    const frames = jsonLines(text).map(frame => ({ direction: frame.direction, wire: object(frame.wire) }));
    const requests = frames.flatMap(({ direction, wire }, index) =>
        direction === 's2c' && wire.type === 'control_request' && field(wire.request, 'subtype') === 'elicitation'
            ? [{ index, id: wire.request_id, request: object(wire.request) }] : []);
    if (requests.length !== 6 || decisions.length !== 6 || new Set(requests.map(request => request.id)).size !== 6) {
        throw new Untriggered('expected six distinct native elicitation requests');
    }
    let after = -1;
    for (const [offset, { index, id, request }] of requests.entries()) {
        const decision = decisions[offset];
        const replies = frames.flatMap(({ direction, wire }, at) =>
            direction === 'c2s' && wire.type === 'control_response' && field(wire.response, 'request_id') === id
                ? [{ at, response: object(wire.response) }] : []);
        if (typeof id !== 'string' || !id || request.mcp_server_name !== 'elicitation_fixture'
            || request.mode !== decision.request.mode || request.message !== decision.request.message
            || replies.length !== 1 || after >= index || index >= replies[0].at
            || replies[0].response.subtype !== 'success'
            || !isDeepStrictEqual(replies[0].response.response, decision.answer)) {
            throw new Untriggered('standalone elicitation lacks a correlated successful native response');
        }
        after = replies[0].at;
    }
    const completion = [{ type: 'text', text: 'SYNTHETIC_ELICITATION_SEQUENCE_DONE' }];
    const observed = frames.slice(after + 1).some(({ direction, wire }) => {
        const content = field(wire.message, 'content');
        return direction === 's2c' && wire.type === 'user' && Array.isArray(content)
            && content.some(item => field(item, 'type') === 'tool_result'
                && isDeepStrictEqual(field(item, 'content'), completion));
    });
    if (!observed) {
        throw new Untriggered('Claude MCP fixture did not observe all elicitation answers');
    }
}
