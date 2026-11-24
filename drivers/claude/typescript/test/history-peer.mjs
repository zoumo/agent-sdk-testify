import assert from 'node:assert/strict';
import { readFileSync, writeFileSync } from 'node:fs';
import { createInterface } from 'node:readline';

const [capture, requestsPath, workspace, config] = process.argv.slice(2);
assert.equal(process.cwd(), workspace);
assert.equal(process.env.CLAUDE_CONFIG_DIR, config);
const rows = readFileSync(capture, 'utf8').trimEnd().split('\n').map(JSON.parse);
const requests = [];
let offset = 0;
let initializeId;
let heldRoot;
const toolIds = new Map();
function incoming() {
    while (rows[offset]?.direction === 's2c') {
        const row = rows[offset++].wire;
        if (row.type === 'control_response' && row.response.request_id === rows[0].wire.request_id) {
            row.response.request_id = initializeId;
        }
        if (process.env.TESTIFY_READ_BEFORE_ROOT === '1' && row.type === 'result' && !heldRoot
            && rows.slice(offset).find(item => item.direction === 'c2s')?.wire.response?.response?.updatedInput?.file_path) {
            heldRoot = row;
            continue;
        }
        if (row.request?.subtype === 'can_use_tool') {
            toolIds.set(row.request_id, row.request.tool_use_id);
        }
        console.log(JSON.stringify(row));
    }
    if (heldRoot) {
        setTimeout(() => {
            console.log(JSON.stringify(heldRoot));
            heldRoot = undefined;
        }, 30);
    }
}
const lines = createInterface({ input: process.stdin });
for await (const line of lines) {
    assert.equal(heldRoot, undefined, 'SDK answered the child Read before the native root result');
    const actual = JSON.parse(line);
    requests.push(actual);
    const expected = rows[offset++];
    assert.equal(expected?.direction, 'c2s', JSON.stringify(actual));
    if (expected.wire.request?.subtype === 'initialize') {
        assert.equal(actual.request?.subtype, 'initialize');
        assert.equal(actual.request?.hooks, undefined);
        if (expected.wire.request.agents) {
            assert.deepEqual(actual.request.agents, expected.wire.request.agents);
        }
        initializeId = actual.request_id;
    } else {
        if (actual.type === 'control_response') {
            // The official TS SDK echoes the native tool ID in its decision.
            const toolId = toolIds.get(expected.wire.response.request_id);
            assert.ok(toolId);
            expected.wire.response.response.toolUseID = toolId;
        }
        assert.deepEqual(actual, expected.wire);
    }
    incoming();
}
assert.equal(offset, rows.length, 'SDK closed before all fixture events and requests');
writeFileSync(requestsPath, JSON.stringify(requests));
