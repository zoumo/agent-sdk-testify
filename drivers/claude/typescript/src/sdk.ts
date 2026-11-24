import { spawn, type ChildProcessByStdio } from 'node:child_process';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { type Readable, Writable } from 'node:stream';
import type { Query, SpawnedProcess, SpawnOptions } from '@anthropic-ai/claude-agent-sdk';
import { object, readObject, type DriverRuntime, type JsonObject, type Peer } from './runtime.js';

const SDK_PACKAGE = '@anthropic-ai/claude-agent-sdk';
const SDK_VERSION = '0.3.293';

export function sdkIdentity() {
    // Resolve the same root export that the SDK import uses. The SDK does not
    // export its package.json as a package subpath.
    const entry = createRequire(import.meta.url).resolve(SDK_PACKAGE);
    const { name, version } = readObject(join(dirname(entry), 'package.json'));
    if (name !== SDK_PACKAGE || version !== SDK_VERSION) {
        throw new Error(`expected ${SDK_PACKAGE}@${SDK_VERSION}`);
    }
    return { package: name, version };
}

interface PeerExit {
    exitCode: number | null;
    signal: NodeJS.Signals | null;
    error?: Error;
}

export function sdkPeer(runtime: DriverRuntime, peer: Peer = runtime.peer, onWrite?: (line: string) => void) {
    let stopped: Promise<PeerExit> | undefined;
    let child: ChildProcessByStdio<Writable, Readable, null> | undefined;
    return {
        spawn({ signal }: SpawnOptions): SpawnedProcess {
            if (child) {
                throw new Error('official SDK attempted to launch the same peer twice');
            }
            // The Go parent supplies an isolated environment. The public
            // launcher's command and argv take precedence over SDK defaults.
            const processPeer = spawn(peer.command, peer.args, {
                cwd: runtime.workspace,
                env: {
                    ...process.env,
                    ...peer.env,
                    HOME: runtime.home,
                    CLAUDE_CONFIG_DIR: runtime.config,
                    TMPDIR: runtime.tmp,
                },
                stdio: ['pipe', 'pipe', 'inherit'],
                signal,
            });
            child = processPeer;
            stopped = new Promise<PeerExit>(resolve => {
                processPeer.once('error', error => {
                    // Abort emits an error before the child is reaped.
                    if (processPeer.pid === undefined) {
                        resolve({ exitCode: null, signal: null, error });
                    }
                });
                processPeer.once('close', (exitCode, signal) => resolve({ exitCode, signal }));
            });
            if (!onWrite) {
                return processPeer;
            }
            return {
                stdin: observeInput(processPeer.stdin, onWrite),
                stdout: processPeer.stdout,
                get killed() { return processPeer.killed; },
                get exitCode() { return processPeer.exitCode; },
                get signalCode() { return processPeer.signalCode; },
                kill: processPeer.kill.bind(processPeer),
                on: processPeer.on.bind(processPeer),
                once: processPeer.once.bind(processPeer),
                off: processPeer.off.bind(processPeer),
            };
        },
        abort() {
            if (child?.exitCode === null && child.signalCode === null) {
                child.kill('SIGTERM');
            }
        },
        async settled() {
            return stopped;
        },
        async wait() {
            if (!stopped) {
                throw new Error('official SDK did not start its public agent peer');
            }
            return stopped;
        },
    };
}

function observeInput(input: Writable, onWrite: (line: string) => void): Writable {
    let pending = '';
    const writer = new Writable({
        decodeStrings: false,
        write(chunk: string, encoding, afterWrite) {
            input.write(chunk, encoding, error => {
                if (!error) {
                    const lines = (pending + chunk).split('\n');
                    pending = lines.pop()!;
                    for (const line of lines) {
                        onWrite(line);
                    }
                }
                afterWrite(error);
            });
        },
        final(done) {
            input.end(done);
        },
    });
    input.on('error', error => writer.destroy(error));
    return writer;
}

export async function getSettings(session: Query): Promise<JsonObject> {
    // The pinned SDK implements this control, but omits it from Query's declarations.
    const query = session as Query & { getSettings?: () => Promise<unknown> };
    if (typeof query.getSettings !== 'function') {
        throw new Error('installed Claude SDK does not expose getSettings');
    }
    return object(await query.getSettings());
}
