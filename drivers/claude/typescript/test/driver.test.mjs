import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const CLI = fileURLToPath(new URL('../dist/cli.js', import.meta.url));

function request(t) {
    const root = realpathSync(mkdtempSync(join(tmpdir(), 'testify-claude-driver-')));
    t.after(() => rmSync(root, { recursive: true, force: true }));
    const materials = join(root, 'materials');
    mkdirSync(materials);
    const path = join(root, 'request.json');
    const runtimePath = join(root, 'runtime.json');
    const marker = join(root, 'peer-started');
    const peer = join(root, 'peer.mjs');
    writeFileSync(peer, `
        import assert from 'node:assert/strict';
        import { writeFileSync } from 'node:fs';
        import { createInterface } from 'node:readline';
        assert.equal(process.env.TESTIFY_PEER, 'isolated');
        assert.equal(process.cwd(), process.argv[3]);
        assert.equal(process.env.HOME, process.argv[3]);
        assert.equal(process.env.CLAUDE_CONFIG_DIR, process.argv[3]);
        assert.equal(process.env.TMPDIR, process.argv[3]);
        writeFileSync(process.argv[2], 'started');
        const lines = createInterface({ input: process.stdin });
        for await (const line of lines) {
            const wire = JSON.parse(line);
            if (wire.request?.subtype === 'initialize') {
                console.log(JSON.stringify({ type: 'control_response', response: {
                    subtype: 'success', request_id: wire.request_id, response: {},
                } }));
            }
            if (wire.type === 'user') {
                console.log(JSON.stringify({ type: 'result', subtype: 'success', is_error: false,
                    session_id: 'synthetic' }));
                lines.close();
                process.stdin.destroy();
                break;
            }
        }
    `);
    writeFileSync(path, JSON.stringify({ case: 'SDK-CASE-042', materials, runtime: runtimePath }));
    writeFileSync(join(materials, 'case.json'), JSON.stringify({
        provider: 'claude', caseId: 'SDK-CASE-042', storyId: 'SDK-US-004',
        scenario: { driverProfile: 'standalone-elicitation', mode: 'live-selected-path',
            prompts: ['synthetic prompt'], requiredRequests: ['standalone-elicitation'] },
    }));
    const runtime = {
        schemaVersion: 1, root, workspace: root, home: root, config: root, raw: root, tmp: root,
        output: root, replay: true, fixtureDir: root, agentResults: { main: join(root, 'main.jsonl') },
        peer: { command: process.execPath, args: [peer, marker, root], env: { TESTIFY_PEER: 'isolated' } },
    };
    return { root, marker, runtime, run(override = {}) {
        writeFileSync(runtimePath, JSON.stringify({ ...runtime, ...override }));
        return spawnSync(process.execPath, [CLI, '--request', path], { encoding: 'utf8', timeout: 10_000 });
    } };
}

test('runtime rejects invalid schema, paths and peer inputs before starting the peer', async t => {
    for (const [name, override, expected] of [
        ['schema', { schemaVersion: 2 }, /runtime configuration version/],
        ['workspace', { workspace: 'recorded/project' }, /absolute workspace/],
        ['argv', { peer: { command: process.execPath, args: [42] } }, /peer args are invalid/],
        ['result', { agentResults: { main: 'main.jsonl' } }, /absolute agentResults.main/],
    ]) {
        await t.test(name, t => {
            const fixture = request(t);
            const child = fixture.run(override);
            assert.equal(child.status, 1, child.stderr);
            assert.match(child.stderr, expected);
            assert.equal(existsSync(fixture.marker), false);
        });
    }
});

test('a successful root and peer exit without callbacks are untriggered', t => {
    const fixture = request(t);
    const child = fixture.run();
    assert.equal(child.status, 0, child.stderr);
    assert.equal(existsSync(fixture.marker), true, child.stderr);
    const result = JSON.parse(readFileSync(join(fixture.root, 'result.json'), 'utf8'));
    const report = JSON.parse(readFileSync(join(fixture.root, 'replay-report.json'), 'utf8'));
    assert.equal(result.status, 'untriggered');
    assert.equal(report.branchStatus, 'untriggered');
    assert.deepEqual(report.peerProcess, { connectionId: 'main', exitCode: 0, signal: null });
    assert.equal(report.results, 1);
    assert.equal(report.requests, 0);
});
