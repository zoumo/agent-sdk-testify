// A single stdio MCP tool exercises both native modes and all decisions.
const { createInterface } = require('node:readline');
const actions = ['form', 'url'].flatMap(mode => ['accept', 'decline', 'cancel'].map(action => ({mode, action})));
const send = message => process.stdout.write(JSON.stringify({jsonrpc: '2.0', ...message}) + '\n');
let toolId;
let next = 0;

function requestInput() {
    if (next === actions.length) {
        send({id: toolId, result: {content: [{type: 'text', text: 'SYNTHETIC_ELICITATION_SEQUENCE_DONE'}]}});
        return;
    }
    const {mode, action} = actions[next];
    const fields = mode === 'form'
        ? {requestedSchema: {type: 'object', properties: {value: {type: 'string'}}, required: ['value']}}
        : {url: 'https://example.invalid/synthetic', elicitationId: `fixture-${next}`};
    send({id: `elicitation-${next}`, method: 'elicitation/create', params: {
        mode, message: `SYNTHETIC ${mode} ${action}`, ...fields,
    }});
}

createInterface({input: process.stdin}).on('line', line => {
    const message = JSON.parse(line);
    if (message.method === 'initialize') {
        send({id: message.id, result: {protocolVersion: message.params.protocolVersion, capabilities: {tools: {}},
            serverInfo: {name: 'elicitation_fixture', version: '1'}}});
    } else if (message.method === 'tools/list') {
        send({id: message.id, result: {tools: [{name: 'roundtrip', description: 'Run the synthetic elicitation fixture.',
            inputSchema: {type: 'object', properties: {}, additionalProperties: false}}]}});
    } else if (message.method === 'tools/call' && message.params?.name === 'roundtrip' && toolId === undefined) {
        toolId = message.id;
        requestInput();
    } else if (toolId !== undefined && next < actions.length && message.id === `elicitation-${next}`) {
        const expected = actions[next];
        const response = message.result;
        if (response?.action !== expected.action || (next === 0 && response.content?.value !== 'SYNTHETIC_FORM')) {
            send({id: toolId, error: {code: -32603, message: 'Unexpected synthetic elicitation reply'}});
            return;
        }
        next++;
        requestInput();
    } else if (message.method === 'ping') {
        send({id: message.id, result: {}});
    }
});
