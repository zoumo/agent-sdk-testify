import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const cli = fileURLToPath(new URL('../dist/cli.js', import.meta.url));
const peer = fileURLToPath(new URL('./interaction-peer.mjs', import.meta.url));
const catalog = fileURLToPath(new URL('../../../../data/scenarios/', import.meta.url));
const read = filename => JSON.parse(readFileSync(filename, 'utf8'));
const bindings = read(join(catalog, 'bindings', 'claude.json')).cases;
const cases = readdirSync(catalog).filter(name => /^SDK-US-\d+\.json$/.test(name))
    .flatMap(name => read(join(catalog, name)).cases);

function run(t, number, variant, expectedStatus) {
    const caseId = 'SDK-CASE-' + number;
    const original = cases.find(c => c.id === caseId);
    const binding = bindings[caseId];
    const root = realpathSync(mkdtempSync(join(tmpdir(), 'testify-claude-interaction-')));
    t.after(() => rmSync(root, { recursive: true, force: true }));
    const paths = { root, workspace: root, home: root, config: root, output: root, tmp: root,
        raw: join(root, 'packages', 'controlled-material', 'material', 'raw') };
    const parameters = { ...original.parameters, ...binding.parameters };
    const model = variant.startsWith('magpie-') ? 'auto/deepseek-v4.1-flash-1017[1m]' : 'synthetic-target';
    let initialModel;
    const args = ['--model', model];
    if (['022', '023', '024'].includes(number)) {
        initialModel = variant === 'magpie-same-model' ? model.replace('[1m]', '') : 'synthetic-initial';
        args[1] = initialModel;
    }
    if (parameters.setting === 'thinking') {
        args.push('--max-thinking-tokens', '1024');
    }
    if (parameters.setting === 'effort') {
        args.push('--effort', parameters.effort);
    }
    if (parameters.injectionTiming) {
        parameters.contextValue = randomUUID();
    }
    const plan = {
        provider: 'claude', caseId, storyId: original.storyId, model, initialModel, args,
        driverProfile: original.driverProfile, stdinEOFAction: number === '030' ? 'interrupt' : 'close',
        scenario: {
            driverProfile: original.driverProfile, ...(binding.execution ?? original.execution), parameters,
            prompts: binding.inputs ?? Object.values(original.inputs),
            configuration: binding.configuration, answers: { ...original.expect.answers, ...binding.answers },
            requiredRequests: original.expect.requests.map(name => binding.requests[name]),
        },
    };
    for (const [file, contents] of Object.entries(original.setup.files)) {
        writeFileSync(join(root, file), contents);
    }
    const runtime = join(root, 'runtime.json');
    const materials = join(root, 'materials');
    const request = join(root, 'request.json');
    mkdirSync(materials);
    for (const file of [join(root, 'case.json'), join(materials, 'case.json')]) {
        writeFileSync(file, JSON.stringify(plan));
    }
    writeFileSync(runtime, JSON.stringify({ schemaVersion: 1, ...paths, replay: true, fixtureDir: root,
        agentResults: { main: join(root, 'main.jsonl') }, peer: { command: process.execPath, args: [peer, root, variant] } }));
    writeFileSync(request, JSON.stringify({ case: caseId, materials, runtime }));
    const child = spawnSync(process.execPath, [cli, '--request', request], {
        env: { PATH: process.env.PATH }, timeout: 12_000, encoding: 'utf8',
    });
    assert.equal(child.status, 0, child.error?.message ?? child.stderr);
    const result = read(join(root, 'result.json'));
    const report = read(join(root, 'replay-report.json'));
    assert.equal(result.status, expectedStatus, JSON.stringify({ result, stderr: child.stderr, trace: report.flowTrace }));
    assert.equal(report.branchStatus, expectedStatus);
    assert.equal(report.captureStatus, expectedStatus === 'failed' ? 'failed' :
        expectedStatus === 'unsupported' ? 'not-executed' : 'complete');
    if (expectedStatus === 'unsupported') {
        assert.equal(existsSync(join(root, 'main.jsonl')), false);
        assert.equal(report.peerProcess, undefined);
    } else {
        assert.deepEqual(result.sdk, { package: '@anthropic-ai/claude-agent-sdk', version: '0.3.293' });
        assert.deepEqual(result.sdk, report.officialSDK);
        assert.equal(report.peerProcess.connectionId, 'main');
    }
    if (expectedStatus === 'passed') {
        assert.deepEqual(report.gaps, []);
        assert.equal(report.peerProcess.exitCode, 0);
        assert.equal(report.peerProcess.signal, null);
        assert.equal(report.peerProcess.executionId, 'controlled-execution');
        assert.equal(report.flowTrace.at(-1).step, 'close-drain');
        assert.equal(report.cliVersion, 'synthetic peer');
        assert.equal(report.inputs, plan.scenario.prompts.length);
    }
    return { result, report, parameters };
}

test('037 and 038 send the original shouldQuery flags and confirm the next root query', async t => {
    for (const number of ['037', '038']) {
        await t.test(number, t => {
            const { report, parameters } = run(t, number, 'default', 'passed');
            assert.equal(report.results, 2);
            assert.equal(report.contextInjection.timing, parameters.injectionTiming);
            assert.equal(report.contextInjection.extraTurnObserved, false);
            assert.equal(report.contextInjection.currentQueryUsedFact, false);
            assert.equal(report.contextInjection.subsequentQueryUsedFact, true);
            assert.equal(report.contextInjection.outputs[1], parameters.contextValue);
            assert.equal(report.receipts.some(r => r['injection-barrier']), number === '037');
        });
    }
});

test('ordinary cases exercise the official SDK against a controlled stream', async t => {
    for (const [number, description, verify] of [
        ['001', 'same-session results and post-result idle before input two', report =>
            assert.ok(report.flowTrace.find(x => x.step === 'post-result-event'))],
        ['002', 'native image and content answer', report =>
            assert.equal(report.receipts.find(x => x['media-content-observed'])['media-content-observed'].kind, 'image')],
        ['004', 'one ordered text/image/text message', report =>
            assert.equal(report.receipts.find(x => x['media-content-observed'])['media-content-observed'].kind, 'mixed')],
        ['005', 'active supplement and second native terminal', report => {
            assert.equal(report.results, 2);
            assert.equal(report.activeInput.outcome, 'separate-execution');
            assert.ok(report.flowTrace.find(x => x.step === 'submit-supplement' && x.outcome === 'written'));
        }],
        ['006', 'correlated read and write results', report =>
            assert.deepEqual(report.receipts.filter(x => x['tool-decision']).map(x => x['tool-decision'].name), ['Read', 'Write'])],
        ['007', 'SDK MCP negotiation and application callback', report =>
            assert.equal(report.requests, 1)],
        ...[['008', 'Write', 'deny'], ['009', 'Write', 'allow'], ['010', 'Bash', 'allow'], ['011', 'Bash', 'deny']].map(
            ([number, tool, behavior]) => [number, tool + ' ' + behavior, report => {
                const receipt = report.receipts.find(x => x['tool-decision'])['tool-decision'];
                assert.equal(receipt.name, tool);
                assert.equal(receipt.behavior, behavior);
                assert.equal(receipt.result.is_error, behavior === 'deny');
            }]),
        ['012', 'permission held across status, mode acknowledgement and restore', report => {
            assert.deepEqual(report.flowTrace.filter(x => x.step === 'request').map(x => x.name), ['Read', 'held-control', 'held-event']);
            assert.equal(report.receipts.find(x => x['held-permission-event'])['held-permission-event'].native.permissionMode, 'plan');
        }],
        ...[['013', 'single choice'], ['014', 'free text'], ['015', 'multiple questions']].map(([number, description]) =>
            [number, description, report => assert.equal(report.receipts.find(x => x['tool-decision'])['tool-decision'].name, 'AskUserQuestion')]),
        ['021', 'launch model and matching native init', report =>
            assert.equal(report.configurationEvidence.observedModel, 'synthetic-target')],
        ...[['022', 'before-first-input', 0], ['023', 'between-inputs', 1], ['024', 'during-execution', 0]].map(
            ([number, timing, completedInputs]) => [number, 'model change ' + timing, report => {
                const [request] = report.configurationEvidence.requests;
                assert.equal(request.timing, timing);
                assert.equal(request.completedInputs, completedInputs);
                assert.equal(request.acknowledged, true);
                assert.deepEqual(new Set(report.configurationEvidence.observedModels),
                    new Set([number === '024' ? 'synthetic-initial' : 'synthetic-target']));
            }]),
        ['025', 'native creation thinking mode', report =>
            assert.equal(report.configurationEvidence.observedMode, 'enabled')],
        ['027', 'native effort preflight and runtime update', report => {
            assert.equal(report.configurationEvidence.preflightEffort, 'low');
            assert.equal(report.configurationEvidence.targetEffort, 'high');
        }],
        ['028', 'native thinking_display and correlated success', report =>
            assert.equal(report.configurationEvidence.requests[0].requested.thinking_display, 'omitted')],
        ['030', 'acknowledged interrupt, root cancellation, idle, and reaped peer', report => {
            assert.equal(report.interruptClose.processClosed, true);
            assert.ok(report.receipts.find(x => x['interrupted-result']));
        }],
        ['031', 'explicit SDK close, rejected submission and reaped peer', report =>
            assert.deepEqual(report.sessionClose, { interactionClosed: true, postCloseRejected: true, processClosed: true })],
    ]) {
        await t.test(number + ' ' + description, t => verify(run(t, number, 'default', 'passed').report));
    }
});

test('negative outcomes preserve native failure, missing evidence and unsupported binding', async t => {
    for (const [number, variant, status, message] of [
        ['003', 'default', 'unsupported', /document/],
        ['002', 'wrong-color', 'untriggered', /image content/],
        ['008', 'denial-ran-tool', 'untriggered', /does not match the approval/],
        ['012', 'missing-held-event', 'untriggered', /no same-session native event/],
        ['021', 'native-error', 'failed', /unsuccessful result/],
        ['023', 'unchanged-model', 'untriggered', /effectiveness/],
        ['025', 'no-thinking', 'untriggered', /observed thinking/],
        ['028', 'reject-configuration', 'failed', /native display refusal/],
        ['030', 'interrupt-completed', 'untriggered', /without acknowledged cancellation/],
        ['037', 'bad-injection-ack', 'untriggered', /zero-output/],
    ]) {
        await t.test(number + ' ' + variant, t => assert.match(run(t, number, variant, status).result.message, message));
    }
});

test('024 retains a native configuration rejection and the original execution result', t => {
    const { report } = run(t, '024', 'reject-during', 'passed');
    assert.equal(report.configurationEvidence.requests[0].acknowledged, false);
    assert.equal(report.configurationEvidence.nativeRejection, 'native refusal');
    assert.equal(report.results, 1);
});

test('023 corroborates a Magpie assistant model alias with usage from the changed execution', t => {
    assert.match(run(t, '023', 'magpie-same-model', 'untriggered').result.message, /distinct explicit/);
    const { report } = run(t, '023', 'magpie-alias', 'passed');
    assert.deepEqual(report.configurationEvidence.observedModels, ['group/auto-deepseek-v4-1-flash-1017']);
    assert.match(run(t, '023', 'magpie-wrong-usage', 'untriggered').result.message, /effectiveness/);
});

test('005 retains a native rejection tied to the supplemental input and the original result', t => {
    const { report } = run(t, '005', 'reject-supplement', 'passed');
    assert.equal(report.activeInput.outcome, 'rejected');
    assert.equal(report.activeInput.terminalReason, 'stop_hook_prevented');
    assert.deepEqual(report.activeInput.errors, ['controlled native input refusal']);
    assert.equal(report.results, 2);
});
