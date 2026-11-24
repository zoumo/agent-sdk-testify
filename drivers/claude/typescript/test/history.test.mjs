import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { cpSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const CLI = fileURLToPath(new URL('../dist/cli.js', import.meta.url));
const PEER = fileURLToPath(new URL('./history-peer.mjs', import.meta.url));
const DATA = fileURLToPath(new URL('../../../../data/claude/', import.meta.url));
const MATERIALS = [
    ['005', '016', '79bf7c7c0187b35059e6853a'],
    ['005', '018', 'bbf6e70474ab6bcd295fbb1f'],
    ['005', '019', '0d291b601e5394381f2eda9b'],
    ['005', '020', 'b16d3ecbd94925d7a3900d72'],
    ['006', '029', '399514e7268a7d6506dd90c1'],
    ['008', '033', '8b081b0c3dfc512f8d01cefa'],
    ['008', '034', '2adbb8b675acd84d35a978fd'],
    ['008', '035', 'b4b026feac023e908929f911'],
    ['008', '035', '8e60944e3531638b79982061'],
    ['008', '036', '5d8dee264c985200fdf6e75f'],
];
const read = path => JSON.parse(readFileSync(path, 'utf8'));
const write = (path, value) => writeFileSync(path, JSON.stringify(value) + '\n');

function isolatedRequest(t, story, id, materialId, adjust = () => {}) {
    const root = realpathSync(mkdtempSync(join(tmpdir(), 'testify-claude-history-')));
    t.after(() => rmSync(root, { recursive: true, force: true }));
    const caseId = `SDK-CASE-${id}`;
    const fixtureId = 'history-fixture';
    const executionId = 'history-execution';
    const source = join(DATA, `SDK-US-${story}`, caseId, materialId);
    const resolvedId = read(join(source, 'material.json')).id;
    const pkg = join(root, 'packages', resolvedId);
    const material = join(pkg, 'material');
    cpSync(source, material, { recursive: true });
    const plan = read(join(material, 'plan.json'));
    adjust(plan, material);
    const connections = plan.existingChildrenSetup ? ['main', 'probe'] : ['main'];
    const workspace = join(root, 'workspace');
    const config = join(root, 'config');
    mkdirSync(workspace);
    mkdirSync(config);
    const claimed = new Set();
    // These copies model the existing Go history materializer's isolated cwd
    // derivation. The test passes original captured frames to the local peer.
    for (const binding of [...plan.historyBindings].reverse()) {
        const suffix = binding.nativeTarget.split('/').slice(3).join('/');
        if (claimed.has(suffix)) {
            continue;
        }
        claimed.add(suffix);
        const path = join(config, 'projects', workspace.replace(/[^A-Za-z0-9]/g, '-'), suffix);
        mkdirSync(join(path, '..'), { recursive: true });
        const input = readFileSync(join(material, binding.source), 'utf8');
        writeFileSync(path, input.split('\n').map(line => {
            if (!line) {
                return line;
            }
            const row = JSON.parse(line);
            if (row.cwd) {
                row.cwd = workspace;
            }
            return JSON.stringify(row);
        }).join('\n'));
    }
    const execution = `executions/${executionId}/execution.json`;
    mkdirSync(join(pkg, execution, '../receipts'), { recursive: true });
    write(join(root, 'fixture.json'), { schemaVersion: 7, fixtureId });
    write(join(pkg, execution), { schemaVersion: 7, fixtureId, executionId, command: 'mock', artifact: { id: resolvedId } });
    const executionSHA256 = createHash('sha256').update(readFileSync(join(pkg, execution))).digest('hex');
    const peers = {};
    const agentResults = {};
    for (const [offset, connectionId] of connections.entries()) {
        const result = `executions/${executionId}/receipts/${connectionId}.result.json`;
        const receipt = `executions/${executionId}/receipts/${connectionId}.json`;
        const raw = join(material, connectionId === 'probe' ? 'remote-probe/raw' : 'raw', 'raw.jsonl');
        write(join(pkg, receipt), { status: 'passed', fullConsumption: true, nativeLaunched: false,
            exitCode: 0, clientEOF: true, cleanupComplete: true,
            recordingSHA256: createHash('sha256').update(readFileSync(raw)).digest('hex') });
        const evidence = { schemaVersion: 3, fixtureId, materialId: resolvedId, executionId, connectionId, processId: offset + 1,
            command: 'mock', execution, result, receipt, executionSHA256 };
        const finished = { ...evidence, status: 'passed' };
        write(join(pkg, result), finished);
        const notices = join(root, `${connectionId}.notices.jsonl`);
        writeFileSync(notices, JSON.stringify({ ...evidence, status: 'running' }) + '\n' + JSON.stringify(finished) + '\n');
        peers[connectionId] = {
            command: process.execPath,
            args: [PEER, raw, join(root, `${connectionId}.wire.json`), workspace, config],
            env: id === '019' ? { TESTIFY_READ_BEFORE_ROOT: '1' } : {},
        };
        agentResults[connectionId] = notices;
    }
    const materials = join(root, 'input');
    mkdirSync(materials);
    write(join(materials, 'case.json'), plan);
    const runtime = { schemaVersion: 1, root, workspace, config, home: root, tmp: root, output: root,
        raw: join(material, 'raw'), seedRoot: material, fixtureDir: root, replay: true, agentResults,
        peer: { ...peers.main, ...(peers.probe ? { probe: peers.probe } : {}) },
        recordedWorkspace: read(join(material, 'manifest.json')).native.cwd };
    const requestPath = join(root, 'request.json');
    write(requestPath, { case: caseId, materials, runtime: join(root, 'runtime.json') });
    return {
        pkg, plan, runtime, material, resolvedId,
        run() {
            write(join(root, 'runtime.json'), runtime);
            const result = spawnSync(process.execPath, [CLI, '--request', requestPath], { encoding: 'utf8', timeout: 8_000, env: {
                ...process.env, CLAUDE_CONFIG_DIR: join(root, 'wrong-config'), CLAUDE_CODE_PROJECT_DIR_NAME: 'wrong-project',
            } });
            assert.equal(result.status, 0, result.error?.message ?? result.stderr);
            return { stderr: result.stderr, status: read(join(root, 'result.json')), report: read(join(root, 'replay-report.json')),
                requests: readdirSync(root).filter(name => name.endsWith('.wire.json')).map(name => read(join(root, name))) };
        },
    };
}

function receipt(report, name) {
    return report.receipts.find(entry => name in entry)?.[name];
}

for (const [story, id, materialId] of MATERIALS) {
    test(`Claude ${id} uses the official TS SDK with a controlled peer (${materialId})`, t => {
        const fixture = isolatedRequest(t, story, id, materialId);
        const { status, report, requests, stderr } = fixture.run();
        assert.equal(status.status, 'passed', [status.message, stderr].filter(Boolean).join('\n'));
        assert.equal(status.sdk.version, '0.3.293');
        assert.equal(receipt(report, 'peer-process').sdkClosed, true);
        assert.equal(receipt(report, 'peer-process').materialId, fixture.resolvedId);
        assert.match(receipt(report, 'peer-process').receiptSHA256, /^[a-f0-9]{64}$/);
        assert.ok(requests.every(frames => frames[0]?.request?.subtype === 'initialize'));
        switch (id) {
            case '016':
            case '019':
                assert.equal(report.childEvidence.completed, true);
                assert.deepEqual(report.childEvidence.agentIds, [report.childEvidence.taskId]);
                assert.equal(receipt(report, 'native-child-lifecycle').finish.status, 'completed');
                assert.equal(requests[0].filter(frame => frame.type === 'control_response').length, 2);
                if (id === '019') {
                    const order = receipt(report, 'background-read-barrier');
                    assert.ok(order.rootAt < order.allowedAt && order.allowedAt < order.doneAt);
                }
                break;
            case '018':
                assert.equal(report.topology.children.length, 2);
                assert.notEqual(report.topology.children[0].taskId, report.topology.children[1].taskId);
                break;
            case '020':
                assert.equal(requests[0][0].request.forwardSubagentText, true);
                assert.equal(receipt(report, 'child-detail-selection').requested, 'suppress');
                assert.ok(receipt(report, 'child-detail-selection').nativeDetailCount > 0);
                assert.ok(receipt(report, 'get_subagent_messages').messages.length > 0);
                break;
            case '029': {
                const evidence = receipt(report, 'child-model-inheritance');
                assert.equal(evidence.requested, fixture.plan.model);
                assert.deepEqual(Object.values(evidence.childObservedModels), [evidence.rootObservedModel]);
                break;
            }
            case '033':
                assert.equal(receipt(report, 'get_session_info').sessionId, receipt(report, 'result').session_id);
                assert.ok(receipt(report, 'get_session_messages').length > 0);
                break;
            case '034':
                assert.equal(receipt(report, 'get_subagent_messages').sessionId, receipt(report, 'result').session_id);
                assert.equal(receipt(report, 'get_subagent_messages').agentId, receipt(report, 'list_subagents').agentIds[0]);
                break;
            case '035':
                assert.equal(report.observationInputs, 0);
                assert.deepEqual(receipt(report, 'existing-children'), receipt(report, 'existing-pre-observation'));
                if (fixture.plan.existingChildrenSetup) {
                    assert.equal(report.setupInputs, 1);
                    assert.equal(report.setupResults, 1);
                    assert.equal(requests[0].filter(frame => frame.type === 'user').length, 1);
                    assert.equal(requests[1].length, 1);
                    assert.equal(report.receipts.filter(entry => 'peer-process' in entry).length, 2);
                    assert.equal(receipt(report, 'existing-observation-boundary').sessionId, receipt(report, 'existing-setup').sessionId);
                } else {
                    assert.equal(requests.length, 1);
                    assert.equal(requests[0].length, 1);
                }
                break;
            case '036': {
                const whole = receipt(report, 'history-whole').messages;
                const slices = report.receipts.flatMap(entry => entry['history-slice'] ?? []);
                assert.ok(whole.length > 1);
                assert.deepEqual(slices.flatMap(slice => slice.messages), whole);
                assert.deepEqual(slices.at(-1).messages, []);
                assert.equal(requests[0].filter(frame => frame.type === 'user').length, 2);
                break;
            }
        }
    });
}

test('035 accepts material-bound legacy saved files and top-level prompts', t => {
    const fixture = isolatedRequest(t, ...MATERIALS[8], (plan, material) => {
        delete plan.scenario.prompts;
        const seed = plan.historySeed;
        delete seed.materialPlan;
        seed.files = {};
        for (const binding of plan.historyBindings.filter(item => item.role === 'seed')) {
            const source = join(material, binding.source);
            const target = join(material, binding.nativeTarget);
            mkdirSync(join(target, '..'), { recursive: true });
            cpSync(source, target);
            seed.files[binding.nativeTarget] = { sha256: binding.sha256 };
        }
    });
    const { status, requests, report } = fixture.run();
    assert.equal(status.status, 'passed', status.message);
    assert.equal(requests[0].length, 1);
    assert.equal(report.observationInputs, 0);
});

test('035 rejects tampered setup history before the probe starts', t => {
    const fixture = isolatedRequest(t, ...MATERIALS[7]);
    const source = fixture.plan.existingChildrenSetup.historyBindings[1].source;
    writeFileSync(join(fixture.material, source), '{}\n');
    const { status, requests } = fixture.run();
    assert.equal(status.status, 'failed');
    assert.match(status.message, /evidence bytes changed/);
    assert.equal(requests.length, 1);
});

test('035 rejects borrowed probe results even with the same material', t => {
    const fixture = isolatedRequest(t, ...MATERIALS[7]);
    const notices = fixture.runtime.agentResults.probe;
    const borrowed = readFileSync(notices, 'utf8').replaceAll('history-execution', 'borrowed-execution');
    const source = join(fixture.pkg, 'executions');
    cpSync(join(source, 'history-execution'), join(source, 'borrowed-execution'), { recursive: true });
    const execution = join(source, 'borrowed-execution', 'execution.json');
    const descriptor = read(execution);
    write(execution, { ...descriptor, executionId: 'borrowed-execution' });
    const nextDigest = createHash('sha256').update(readFileSync(execution)).digest('hex');
    const rows = borrowed.trimEnd().split('\n').map(line => ({ ...JSON.parse(line), executionSHA256: nextDigest }));
    writeFileSync(notices, rows.map(row => JSON.stringify(row) + '\n').join(''));
    write(join(source, 'borrowed-execution', 'receipts', 'probe.result.json'), rows[1]);
    const { status } = fixture.run();
    assert.equal(status.status, 'failed');
    assert.match(status.message, /did not use its own completed setup execution/);
});
