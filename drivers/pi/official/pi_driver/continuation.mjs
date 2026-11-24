import { readFileSync } from 'node:fs';
import { fileURLToPath, pathToFileURL } from 'node:url';

const input = JSON.parse(readFileSync(0, 'utf8'));
const { RpcClient } = await import(pathToFileURL(input.sdk).href);
const client = new RpcClient({
    cwd: input.cwd,
    cliPath: fileURLToPath(new URL('./peer.mjs', import.meta.url)),
    env: { PI_TESTIFY_PEER: JSON.stringify(input.peer) },
});
const turns = [];
let sessionId;
try {
    await client.start();
    ({ sessionId } = await client.getState());
    if (!sessionId) throw new Error('Pi session has no native identity');
    for (const prompt of input.prompts) {
        const events = await client.promptAndWait(prompt);
        const terminal = events.at(-1);
        const message = events.findLast(
            event => event.type === 'message_end' && event.message?.role === 'assistant',
        )?.message;
        if (terminal?.type !== 'agent_settled' || message?.stopReason !== 'stop') {
            throw new Error('Pi did not settle with a successful assistant message');
        }
        const reply = message.content
            .filter(block => block.type === 'text')
            .map(block => block.text)
            .join('');
        if (!reply.trim()) throw new Error('Pi produced no assistant text');
        turns.push({
            sessionId,
            reply,
            model: message.model,
            provider: message.provider,
            stopReason: message.stopReason,
            terminal: terminal.type,
            events: events.length,
        });
    }
    if ((await client.getState()).sessionId !== sessionId) throw new Error('Pi session identity changed');
} finally {
    await client.stop();
}
process.stdout.write(JSON.stringify({ sessionId, turns }));
