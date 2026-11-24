import type { EffortLevel, Query } from '@anthropic-ai/claude-agent-sdk';
import { isDeepStrictEqual } from 'node:util';
import { Untriggered } from './evidence.js';
import { getSettings } from './sdk.js';
import { blocks, control, fields, one, ordered, reply, rootFrames, type Frames } from './wire.js';
import type { JsonObject } from './runtime.js';

const DEEPSEEK_ROUTE = 'auto/deepseek-v4.1-flash-1017';
const DEEPSEEK_ASSISTANT_MODEL = 'group/auto-deepseek-v4-1-flash-1017';

function isDeepSeekRoute(model: unknown): boolean {
    return model === DEEPSEEK_ROUTE || model === DEEPSEEK_ROUTE + '[1m]';
}

function flag(args: unknown, name: string): unknown[] {
    if (!Array.isArray(args)) {
        throw new Error('material lacks the native launch arguments');
    }
    const passthrough = args.indexOf('--');
    if (passthrough >= 0) {
        args = args.slice(0, passthrough);
    }
    const values = args as unknown[];
    return values.flatMap((value, i) => value === name ? [values[i + 1]]
        : typeof value === 'string' && value.startsWith(name + '=') ? [value.slice(name.length + 1)] : []);
}

export function configurationFor(plan: JsonObject, report: JsonObject, trace: JsonObject[]) {
    const scenario = fields(plan.scenario);
    const params = fields(scenario.parameters);
    const bound = fields(scenario.configuration);
    const setting = params.setting ?? (bound.modelSource ? 'model' : undefined);
    const timing = params.configurationTiming ?? bound.timing;
    if (!setting) {
        return undefined;
    }
    const requests: JsonObject[] = [];
    const evidence: JsonObject = { requests, observed: [], effectiveScope: 'unknown' };
    report.configurationEvidence = evidence;
    let preflight: JsonObject | undefined;

    async function apply(session: Query, completedInputs: number) {
        let requested: JsonObject;
        let rejection: string | undefined;
        switch (setting) {
        case 'model': {
            const model = plan.model;
            if (typeof model !== 'string' || !model || typeof plan.initialModel !== 'string'
                || !plan.initialModel || plan.initialModel === model
                || (isDeepSeekRoute(plan.initialModel) && isDeepSeekRoute(model))) {
                throw new Untriggered('model change requires distinct explicit initial and target models');
            }
            requested = { subtype: 'set_model', model };
            try {
                await session.setModel(model);
            } catch (error) {
                if (timing !== 'during-execution') {
                    throw error;
                }
                rejection = error instanceof Error ? error.message : String(error);
            }
            break;
        }
        case 'effort': {
            const { effort, targetEffort } = params;
            if (typeof targetEffort !== 'string' || !['low', 'medium', 'high', 'xhigh', 'max'].includes(targetEffort)
                || !effort || targetEffort === effort) {
                throw new Untriggered('effort change requires distinct native creation and runtime values');
            }
            const { effort: applied, ultracode } = fields((await getSettings(session)).applied);
            if (applied !== effort || typeof ultracode !== 'boolean') {
                throw new Untriggered('get_settings did not report the configured creation effort and ultracode');
            }
            preflight = { effort: applied, ultracode };
            const settings = { effortLevel: targetEffort as EffortLevel, ultracode: targetEffort === 'xhigh' && ultracode };
            requested = { subtype: 'apply_flag_settings', settings };
            await session.applyFlagSettings(settings);
            break;
        }
        case 'display':
            requested = { subtype: 'set_max_thinking_tokens', max_thinking_tokens: 1024, thinking_display: 'omitted' };
            await session.setMaxThinkingTokens(1024, 'omitted');
            break;
        default:
            throw new Error('unsupported runtime configuration projection');
        }
        const step = rejection === undefined ? 'configuration-acknowledged' : 'configuration-rejected';
        const decision = { requested, timing, completedInputs, acknowledged: rejection === undefined, rejection };
        requests.push(decision);
        trace.push({ step, ...decision });
    }

    function assess(frames: Frames, args: unknown) {
        const inputs = rootFrames(frames, 'c2s', 'user');
        const results = rootFrames(frames, 's2c', 'result');
        const assistants = rootFrames(frames, 's2c', 'assistant');
        evidence.observed = assistants.flatMap(f => {
            const model = fields(f.wire.message).model;
            return typeof model === 'string' ? [{ source: 'assistant', scope: 'assistant-message', model }] : [];
        });
        if (timing === 'creation') {
            const inits = rootFrames(frames, 's2c', 'system').filter(f => f.wire.subtype === 'init'
                && ordered(inputs[0].index, f.index, results[0].index));
            if (setting === 'model') {
                if (typeof plan.model !== 'string' || !plan.model || !isDeepStrictEqual(flag(args, '--model'), [plan.model])
                    || !inits.some(f => f.wire.model === plan.model && f.wire.session_id === results[0].wire.session_id)) {
                    throw new Untriggered('creation model lacks an explicit launch argument and matching native init');
                }
                Object.assign(evidence, { creationOperation: 'claude --model', requestedModel: plan.model,
                    observedModel: plan.model, effectiveScope: 'native session init after explicit model launch' });
            } else {
                const thinking = assistants.find(f => blocks(f.wire).some(b => b.type === 'thinking' || b.type === 'redacted_thinking'));
                if (!isDeepStrictEqual(flag(args, '--max-thinking-tokens'), ['1024']) || !thinking
                    || !inits.some(f => f.wire.session_id === thinking.wire.session_id)) {
                    throw new Untriggered('creation thinking requires the native enabled encoding and observed thinking');
                }
                Object.assign(evidence, { creationOperation: 'claude --max-thinking-tokens', requestedMode: 'enabled',
                    nativeArgument: 1024, observedMode: 'enabled', effectiveScope: 'native thinking in this execution' });
            }
            return;
        }
        const decision = requests[0];
        if (requests.length !== 1 || !decision) {
            throw new Untriggered('native configuration timing barrier was not reached');
        }
        const requested = fields(decision.requested);
        const request = one(control(frames, 'c2s', requested.subtype as string), 'configuration request');
        const answer = reply(frames, request);
        const acknowledged = decision.acknowledged === true;
        if (!isDeepStrictEqual(request.wire.request, requested)
            || answer.response.subtype !== (acknowledged ? 'success' : 'error')) {
            throw new Untriggered('configuration request or correlated SDK outcome differs from native evidence');
        }
        if (timing === 'before-first-input' && !ordered(request.index, answer.index, inputs[0].index)) {
            throw new Untriggered('configuration response did not precede first input');
        }
        if (timing === 'between-inputs' && !ordered(inputs[0].index, results[0].index,
            request.index, answer.index, inputs[1].index, results[1].index)) {
            throw new Untriggered('configuration was not applied between distinct executions');
        }
        if (timing === 'during-execution' && !rootFrames(frames, 's2c', 'stream_event').some(
            f => ordered(inputs[0].index, f.index, request.index, results[0].index))) {
            throw new Untriggered('configuration request lacks native progress before the execution result');
        }
        if (setting === 'model') {
            if (!isDeepStrictEqual(flag(args, '--model'), [plan.initialModel])) {
                throw new Untriggered('native launch arguments do not prove the initial model');
            }
            const after = timing === 'between-inputs' ? inputs[1].index : inputs[0].index;
            const observedModels = assistants.filter(f => f.index > (timing === 'during-execution' ? request.index : after)
                && f.wire.session_id === results.at(-1)?.wire.session_id)
                .map(f => fields(f.wire.message).model).filter(model => typeof model === 'string');
            Object.assign(evidence, { initialModel: plan.initialModel, targetModel: plan.model, observedModels });
            if (timing !== 'during-execution') {
                const model = plan.model as string;
                const nativeModel = observedModels.find(observed => observed === model
                    || (model.startsWith('theta/') && observed === model.slice('theta/'.length)));
                const gatewayInit = rootFrames(frames, 's2c', 'system').find(f => f.wire.subtype === 'init'
                    && f.wire.model === model && f.wire.session_id === results.at(-1)?.wire.session_id
                    && ordered(after, f.index, results.at(-1)!.index));
                // Captures of this gateway route report the requested ID in
                // init and the upstream ID in assistant output.
                const gatewayModel = isDeepSeekRoute(model) && gatewayInit
                    && observedModels.includes(DEEPSEEK_ASSISTANT_MODEL);
                if (!nativeModel && !gatewayModel) {
                    throw new Untriggered('target model effectiveness is not established by native assistant messages');
                }
                evidence.effectiveScope = gatewayModel && !nativeModel
                    ? 'target gateway route in session init and upstream assistant model after change'
                    : 'assistant-message model after change';
                if (gatewayModel && !nativeModel) {
                    evidence.observedGatewayRoute = gatewayInit.wire.model;
                }
            } else if (acknowledged) {
                evidence.effectiveScope = 'current execution model is measured by assistant messages';
            } else {
                if (typeof answer.response.error !== 'string' || !answer.response.error) {
                    throw new Untriggered('native configuration rejection lacks its reason');
                }
                evidence.nativeRejection = answer.response.error;
            }
        } else if (setting === 'effort') {
            const settings = one(control(frames, 'c2s', 'get_settings'), 'effort preflight');
            const response = reply(frames, settings);
            const applied = fields(fields(response.response.response).applied);
            if (!isDeepStrictEqual(flag(args, '--effort'), [params.effort]) || response.response.subtype !== 'success'
                || !isDeepStrictEqual(preflight, { effort: applied.effort, ultracode: applied.ultracode })
                || !ordered(results[0].index, settings.index, response.index, request.index)
                || control(frames, 'c2s', 'set_max_thinking_tokens').length !== 0) {
                throw new Untriggered('creation effort and ordered native get_settings preflight are incomplete');
            }
            Object.assign(evidence, { initialEffort: params.effort, targetEffort: params.targetEffort,
                creationOperation: 'claude --effort', runtimeOperation: 'apply_flag_settings',
                preflightEffort: applied.effort, preflightUltracode: applied.ultracode,
                effectiveScope: 'creation argument and pre-update settings observed; runtime update acknowledged' });
        } else {
            evidence.effectiveScope = 'native display setting acknowledged';
        }
    }

    return { timing, apply, assess };
}
