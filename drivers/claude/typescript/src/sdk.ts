import { spawn, type ChildProcessByStdio } from 'node:child_process';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import type { Readable, Writable } from 'node:stream';
import type { SpawnOptions } from '@anthropic-ai/claude-agent-sdk';
import { readObject, type DriverRuntime, type Peer } from './runtime.js';

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

export function sdkPeer(runtime: DriverRuntime, peer: Peer = runtime.peer) {
    let stopped: Promise<PeerExit> | undefined;
    let child: ChildProcessByStdio<Writable, Readable, null> | undefined;
    return {
        spawn({ signal }: SpawnOptions) {
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
            return processPeer;
        },
        async wait() {
            if (!stopped) {
                throw new Error('official SDK did not start its public agent peer');
            }
            return stopped;
        },
    };
}
