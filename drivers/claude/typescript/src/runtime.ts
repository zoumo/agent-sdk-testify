import { readFileSync } from 'node:fs';
import { isAbsolute } from 'node:path';

export type JsonObject = Record<string, unknown>;

export function object(value: unknown): JsonObject {
    if (typeof value !== 'object' || value === null || Array.isArray(value)) {
        throw new Error('expected a JSON object');
    }
    return value as JsonObject;
}

export function readObject(path: string): JsonObject {
    return object(JSON.parse(readFileSync(path, 'utf8')));
}

export function absolutePath(value: unknown, name: string): string {
    if (typeof value !== 'string' || !isAbsolute(value) || value.includes('\0')) {
        throw new Error(`official driver requires absolute ${name}`);
    }
    return value;
}

export interface Peer {
    command: string;
    args: string[];
    env: Record<string, string>;
}

interface Runtime {
    root: string;
    workspace: string;
    home: string;
    config: string;
    raw: string;
    tmp: string;
    output: string;
    fixtureDir?: string;
    seedRoot?: string;
    recordedWorkspace?: string;
    replay: boolean;
    peer: Peer;
    probe?: Peer;
    agentResults: Record<string, string>;
}

function peer(value: unknown): Peer {
    const config = object(value);
    const command = absolutePath(config.command, 'peer command');
    const args = config.args === undefined ? [] : config.args;
    if (!Array.isArray(args) || args.some(arg => typeof arg !== 'string' || arg.includes('\0'))) {
        throw new Error('official driver runtime peer args are invalid');
    }
    const env = config.env === undefined || config.env === null ? {} : object(config.env);
    for (const [name, value] of Object.entries(env)) {
        if (!name || name.includes('=') || name.includes('\0') || typeof value !== 'string' || value.includes('\0')) {
            throw new Error('official driver runtime peer env is invalid');
        }
    }
    return { command, args, env: env as Record<string, string> };
}

export function readRuntime(path: string): Runtime {
    const config = readObject(path);
    if (config.schemaVersion !== 1) {
        throw new Error('unsupported official driver runtime configuration version');
    }
    if (typeof config.replay !== 'boolean') {
        throw new Error('official driver runtime requires replay');
    }
    const peers = object(config.peer);
    const results = config.agentResults === undefined ? {} : object(config.agentResults);
    const agentResults = Object.fromEntries(Object.entries(results).map(([id, value]) => [
        id, absolutePath(value, `agentResults.${id}`),
    ]));
    return {
        root: absolutePath(config.root, 'root'),
        workspace: absolutePath(config.workspace, 'workspace'),
        home: absolutePath(config.home, 'home'),
        config: absolutePath(config.config, 'config'),
        raw: absolutePath(config.raw, 'raw'),
        tmp: absolutePath(config.tmp, 'tmp'),
        output: absolutePath(config.output, 'output'),
        fixtureDir: config.fixtureDir === undefined ? undefined : absolutePath(config.fixtureDir, 'fixtureDir'),
        seedRoot: config.seedRoot === undefined ? undefined : absolutePath(config.seedRoot, 'seedRoot'),
        recordedWorkspace: config.recordedWorkspace === undefined
            ? undefined : absolutePath(config.recordedWorkspace, 'recordedWorkspace'),
        replay: config.replay,
        peer: peer(peers),
        probe: peers.probe === undefined ? undefined : peer(peers.probe),
        agentResults,
    };
}

export type DriverRuntime = ReturnType<typeof readRuntime>;
