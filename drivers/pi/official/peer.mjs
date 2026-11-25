// RpcClient launches a Node script and stops it with SIGTERM. Close the
// recorder's input so it can seal the same execution as any stdio consumer.
import { spawn } from 'node:child_process';

const [executable, ...args] = JSON.parse(process.env.PI_TESTIFY_PEER);
const child = spawn(executable, args, {stdio: ['pipe', 'inherit', 'inherit']});
process.stdin.pipe(child.stdin);
process.on('SIGTERM', () => {
    process.stdin.unpipe(child.stdin);
    process.stdin.pause();
    child.stdin.end();
});
child.stdin.on('error', error => {
    if (error.code !== 'EPIPE') throw error;
});
child.once('exit', (code, signal) => process.exit(signal ? 1 : code ?? 1));
