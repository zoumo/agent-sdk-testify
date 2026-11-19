import { spawn } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

const input = JSON.parse(readFileSync(0, 'utf8'));
const { query } = await import(pathToFileURL(input.sdk));
const decisions = ['form', 'url'].flatMap(mode => ['accept', 'decline', 'cancel'].map(action => ({mode, action})));
const receipts = [];
let native;
let result;
let events = 0;
let resolveInitialized;
const initialized = new Promise(resolve => {
    resolveInitialized = resolve;
});

async function* prompt() {
    await initialized;
    yield {type: 'user', session_id: 'default', parent_tool_use_id: null, message: {
        role: 'user', content: [{type: 'text', text: input.prompt}],
    }};
}

const session = query({prompt: prompt(), options: {
    cwd: input.cwd,
    pathToClaudeCodeExecutable: input.command,
    spawnClaudeCodeProcess: ({signal}) => {
        native = spawn(input.command, input.args, {cwd: input.cwd, stdio: ['pipe', 'pipe', 'inherit'], signal});
        return native;
    },
    canUseTool: async (name, values) => name === 'mcp__elicitation_fixture__roundtrip'
        ? {behavior: 'allow', updatedInput: values}
        : {behavior: 'deny', message: 'Unexpected tool in synthetic elicitation case'},
    onElicitation: async request => {
        const expected = decisions[receipts.length];
        if (!expected || request.serverName !== 'elicitation_fixture' || request.mode !== expected.mode
                || request.message !== `SYNTHETIC ${expected.mode} ${expected.action}`) {
            throw new Error('unexpected synthetic elicitation request');
        }
        const answer = receipts.length === 0
            ? {action: expected.action, content: {value: 'SYNTHETIC_FORM'}} : {action: expected.action};
        receipts.push({request, answer});
        return answer;
    },
}});
resolveInitialized(session.initializationResult());

try {
    for await (const message of session) {
        events++;
        if (message.type === 'result') result = message;
    }
} finally {
    session.close();
}
if (native.exitCode === null && native.signalCode === null) {
    await new Promise(resolve => native.once('exit', resolve));
}
if (native.exitCode !== 0) throw new Error('public agent peer exited unsuccessfully');
process.stdout.write(JSON.stringify({receipts, result, events}));
