import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { createInterface } from 'node:readline';

const [root, variant = 'default'] = process.argv.slice(2);
const read = name => JSON.parse(readFileSync(join(root, name), 'utf8'));
const { caseId, model, initialModel, args, scenario } = read('case.json');
const { parameters: params, prompts, driverProfile: profile } = scenario;
const session = 'synthetic-root';
const frames = [];
let query = 0;
let modelInUse = initialModel ?? model;
let context;
let emitted = 0;
let stagedTool;
let changedMode;
let initialized = false;
const peerRequests = new Map();

function output(wire) {
    frames.push({ direction: 's2c', wire });
    console.log(JSON.stringify(wire));
}

function event(type, body) {
    output({ type, uuid: 'event-' + ++emitted, session_id: session, ...body });
}

function request(id, body) {
    peerRequests.set(id, body);
    output({ type: 'control_request', request_id: id, request: body });
}

function ack(id, entries = {}) {
    output({ type: 'control_response', response: { subtype: 'success', request_id: id, response: entries } });
}

function mcp(id, method, params = {}) {
    request('mcp-' + id, { subtype: 'mcp_message', server_name: 'fixture',
        message: { jsonrpc: '2.0', id, method, params } });
}

function assistant(content) {
    event('assistant', { parent_tool_use_id: null, message: { model: modelInUse, content } });
}

function init() {
    event('system', { subtype: 'init', permissionMode: 'default', model: modelInUse });
}

function progress() {
    event('stream_event', { parent_tool_use_id: null, event: { type: 'message_start' } });
}

function idle() {
    event('system', { subtype: 'session_state_changed', state: 'idle' });
}

function finish(text, queued = 0) {
    assistant([{ type: 'text', text }]);
    event('result', { subtype: 'success', is_error: false, num_turns: 1, result: text, queued_turn_count: queued });
    if (!queued) {
        idle();
    }
}

function acknowledgeInjection() {
    event('result', { subtype: 'success', is_error: false, num_turns: 0,
        result: variant === 'bad-injection-ack' ? 'unexpected output' : '', duration_api_ms: 0, usage: { output_tokens: 0 } });
    idle();
}

function invoke(name, input, enforcePermission = true) {
    const id = 'tool-' + (emitted + 1);
    stagedTool = { name, input, id };
    assistant([{ type: 'tool_use', name, id, input }]);
    if (enforcePermission) {
        request('permission-' + id, { subtype: 'can_use_tool', tool_name: name, tool_use_id: id, input });
    }
}

function toolResult(content, extra, rejected = false) {
    assert.ok(stagedTool);
    event('user', { parent_tool_use_id: null, message: { role: 'user', content: [
        { type: 'tool_result', tool_use_id: stagedTool.id, content, is_error: rejected },
    ] }, tool_use_result: extra });
    stagedTool = undefined;
}

function approved(response) {
    const { name, input } = stagedTool;
    const deny = params.approvalDecision === 'deny';
    assert.equal(response.behavior, deny ? 'deny' : 'allow');
    if (name === 'AskUserQuestion') {
        const answers = Object.fromEntries(input.questions.map(q => [q.question, scenario.answers.choice]));
        assert.deepEqual(response.updatedInput, { ...input, answers });
        toolResult('Questions answered', { answers });
    } else if (name === 'Read') {
        if (profile === 'deferred-approval') {
            assert.equal(changedMode, 'default');
        }
        assert.deepEqual(response.updatedInput, input);
        toolResult('7\n11\n', { file: { content: '7\n11\n' } });
    } else if (deny) {
        assert.equal(response.message, 'Synthetic denial; do not retry');
        toolResult(response.message, { denied: true }, variant !== 'denial-ran-tool');
    } else {
        assert.deepEqual(response.updatedInput, input);
        toolResult(name === 'Bash' ? 'SYNTHETIC_COMMAND' : 'File created successfully', {});
    }
    if (name === 'Read' && profile === 'fixture-tools') {
        invoke('Write', { file_path: join(root, 'summary.txt'), content: 'total=18\n' });
    } else {
        finish('Done');
    }
}

function user(wire) {
    const text = typeof wire.message.content === 'string' ? wire.message.content : wire.message.content[0].text;
    if (profile === 'context-injection') {
        assert.equal(typeof wire.shouldQuery, 'boolean');
        if (!wire.shouldQuery) {
            assert.equal(query, 1);
            assert.equal(text, 'The stored vault code is ' + params.contextValue + '.');
            context = params.contextValue;
            if (params.injectionTiming === 'during-execution') {
                finish('UNKNOWN');
                acknowledgeInjection();
            }
            return;
        }
    } else {
        assert.equal(wire.shouldQuery, undefined);
    }
    assert.equal(text, prompts[query]);
    query++;
    if (query === 1) {
        init();
    }
    if (query === 1 && variant === 'native-error') {
        event('result', { subtype: 'error_during_execution', is_error: true, errors: ['synthetic native failure'] });
        return;
    }
    if (profile === 'context-injection') {
        if (query === 1 && params.injectionTiming === 'during-execution') {
            progress();
        } else {
            finish(query === 1 ? 'Ready' : context);
        }
    } else if (profile === 'active-input') {
        if (query === 1) {
            progress();
        } else {
            // The native peer can start the supplement despite reporting a
            // zero queued count in the first result.
            assistant([{ type: 'text', text: 'First execution' }]);
            event('result', { subtype: 'success', is_error: false, num_turns: 1, result: 'First execution', queued_turn_count: 0 });
            if (variant === 'reject-supplement') {
                assert.ok(wire.uuid);
                event('result', { subtype: 'error_during_execution', is_error: true, user_message_uuid: wire.uuid,
                    terminal_reason: 'stop_hook_prevented', errors: ['controlled native input refusal'] });
                idle();
            } else {
                finish('Includes sample');
            }
        }
    } else if (profile === 'interrupt' || params.configurationTiming === 'during-execution') {
        progress();
        if (variant === 'interrupt-completed') {
            finish('Already done');
        }
    } else if (params.inputKind) {
        const content = wire.message.content;
        assert.deepEqual(content.map(b => b.type), params.inputKind === 'mixed' ? ['text', 'image', 'text'] : ['text', 'image']);
        assert.equal(content[1].source.media_type, 'image/png');
        assert.equal(Buffer.from(content[1].source.data, 'base64').subarray(1, 4).toString(), 'PNG');
        finish(variant === 'wrong-color' ? 'red' : 'Blue.');
    } else if (params.applicationTool) {
        assert.equal(initialized, true, 'tool negotiation must precede input');
        invoke('mcp__fixture__fixture_read', { path: 'numbers.txt' }, false);
        mcp(2, 'tools/call', { name: 'fixture_read', arguments: { path: 'numbers.txt' },
            _meta: { 'claudecode/toolUseId': stagedTool.id } });
    } else if (['fixture-tools', 'deferred-approval'].includes(profile)) {
        invoke('Read', { file_path: join(root, 'numbers.txt') });
    } else if (profile === 'approval') {
        if (params.approvalKind === 'command') {
            invoke('Bash', { command: 'printf SYNTHETIC_COMMAND' });
        } else {
            const deny = params.approvalDecision === 'deny';
            invoke('Write', { file_path: join(root, deny ? 'denied.txt' : 'approved.txt'),
                content: deny ? 'denied\n' : 'SYNTHETIC_APPROVED' });
        }
    } else if (profile === 'questions') {
        const { options, question } = scenario.answers;
        const titles = params.questionCount === 2 ? ['Which color?', 'Which second color?']
            : params.freeText ? ['What short note should be recorded?'] : [question];
        invoke('AskUserQuestion', { questions: titles.map(name => ({
            question: name, options: (options.length ? options : ['No note', 'Placeholder']).map(label => ({ label })),
        })) });
    } else {
        if (params.setting === 'thinking' && variant !== 'no-thinking') {
            assistant([{ type: 'thinking', thinking: 'synthetic', signature: 'synthetic' }]);
        }
        finish(query === 1 ? 'Ready' : 'Done');
    }
}

function controlRequest(wire) {
    const { request: req, request_id: id } = wire;
    switch (req.subtype) {
    case 'initialize':
        assert.equal(Object.hasOwn(req, 'hooks'), false);
        if (params.applicationTool) {
            assert.ok(req.sdkMcpServers.includes('fixture'));
            mcp(0, 'initialize', { protocolVersion: '2025-11-25', capabilities: {},
                clientInfo: { name: 'controlled-test-peer', version: '1' } });
        }
        ack(id);
        break;
    case 'get_settings':
        if (profile === 'context-injection') {
            assert.equal(context, params.contextValue);
            assert.equal(query, 1);
            ack(id);
            acknowledgeInjection();
        } else {
            assert.equal(query, 1);
            ack(id, { applied: { effort: params.effort, ultracode: false } });
        }
        break;
    case 'set_model':
        assert.equal(req.model, model);
        assert.equal(query, caseId.endsWith('022') ? 0 : 1);
        if (variant === 'reject-configuration' || variant === 'reject-during') {
            output({ type: 'control_response', response: { subtype: 'error', request_id: id, error: 'native refusal' } });
        } else {
            ack(id);
            if (variant !== 'unchanged-model' && params.configurationTiming !== 'during-execution') {
                modelInUse = req.model;
            }
        }
        if (params.configurationTiming === 'during-execution') {
            finish('Original execution completed');
        }
        break;
    case 'apply_flag_settings':
        assert.equal(query, 1);
        assert.deepEqual(req.settings, { effortLevel: params.targetEffort, ultracode: false });
        ack(id);
        break;
    case 'set_max_thinking_tokens':
        assert.equal(query, 0);
        assert.equal(req.thinking_display, 'omitted');
        assert.equal(req.max_thinking_tokens, 1024);
        if (variant === 'reject-configuration') {
            output({ type: 'control_response', response: { subtype: 'error', request_id: id, error: 'native display refusal' } });
        } else {
            ack(id);
        }
        break;
    case 'set_permission_mode':
        assert.equal(stagedTool?.name, 'Read');
        assert.equal(req.mode, changedMode ? 'default' : 'plan');
        changedMode = req.mode;
        if (variant !== 'missing-held-event' || changedMode === 'default') {
            event('system', { subtype: 'status', permissionMode: req.mode });
        }
        ack(id, { mode: req.mode });
        break;
    case 'interrupt':
        assert.equal(query, 1);
        ack(id);
        if (variant !== 'interrupt-completed') {
            event('result', { subtype: 'error_during_execution', is_error: true,
                terminal_reason: 'aborted_streaming', queued_turn_count: 0, errors: ['synthetic cancellation'] });
            idle();
        }
        break;
    default:
        throw new Error('unexpected control: ' + req.subtype);
    }
}

const lines = createInterface({ input: process.stdin });
for await (const line of lines) {
    const wire = JSON.parse(line);
    frames.push({ direction: 'c2s', wire });
    if (wire.type === 'user') {
        user(wire);
    } else if (wire.type === 'control_request') {
        controlRequest(wire);
    } else if (wire.type === 'control_response') {
        const pending = peerRequests.get(wire.response.request_id);
        assert.ok(pending, 'response must preserve request identity');
        peerRequests.delete(wire.response.request_id);
        assert.equal(wire.response.subtype, 'success');
        const response = wire.response.response;
        if (pending.subtype === 'can_use_tool') {
            if (variant === 'missing-held-event') {
                assert.equal(response.behavior, 'deny');
                toolResult('held evidence absent', { denied: true }, true);
                finish('Done');
            } else {
                approved(response);
            }
        } else {
            const { mcp_response: mcpResponse } = response;
            assert.equal(mcpResponse.id, pending.message.id);
            assert.ok(mcpResponse.result);
            if (pending.message.id === 0) {
                mcp(1, 'tools/list');
            } else if (pending.message.id === 1) {
                assert.equal(mcpResponse.result.tools[0].name, 'fixture_read');
                initialized = true;
            } else {
                assert.deepEqual(mcpResponse.result.content, [{ type: 'text', text: '7\n11\n' }]);
                toolResult(mcpResponse.result.content, mcpResponse.result.content);
                finish('7 and 11');
            }
        }
    }
}

// The controlled peer supplies the two public launcher notifications. The
// driver still verifies them and reads the unmodified bytes sent by the SDK.
const fixtureId = 'controlled-fixture';
const executionId = 'controlled-execution';
const materialId = 'controlled-material';
const pkg = join(root, 'packages', materialId);
const execution = 'executions/' + executionId + '/execution.json';
const result = 'executions/' + executionId + '/receipts/main.result.json';
const declaration = JSON.stringify({ schemaVersion: 7, fixtureId, executionId, command: 'mock', artifact: { id: materialId } });
const notice = { schemaVersion: 3, fixtureId, executionId, materialId, connectionId: 'main', command: 'mock',
    processId: process.pid, execution, result, executionSHA256: createHash('sha256').update(declaration).digest('hex') };
mkdirSync(join(pkg, 'material', 'raw'), { recursive: true });
mkdirSync(join(pkg, 'executions', executionId, 'receipts'), { recursive: true });
writeFileSync(join(root, 'fixture.json'), JSON.stringify({ schemaVersion: 7, fixtureId }));
writeFileSync(join(pkg, execution), declaration);
writeFileSync(join(pkg, result), JSON.stringify({ ...notice, status: 'passed' }));
writeFileSync(join(pkg, 'executions', executionId, 'receipts', 'main.json'),
    JSON.stringify({ fullConsumption: true, clientEOF: true, nativeLaunched: false, cleanupComplete: true }));
writeFileSync(join(pkg, 'material', 'plan.json'), JSON.stringify({ provider: 'claude', caseId, args, cliVersion: 'synthetic peer' }));
writeFileSync(join(pkg, 'material', 'raw', 'raw.jsonl'), frames.map(frame => JSON.stringify(frame)).join('\n') + '\n');
writeFileSync(join(root, 'main.jsonl'), [{ ...notice, status: 'running' }, { ...notice, status: 'passed' }]
    .map(value => JSON.stringify(value)).join('\n') + '\n');
