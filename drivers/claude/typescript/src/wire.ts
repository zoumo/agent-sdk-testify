import { Untriggered, type readWire } from './evidence.js';
import type { JsonObject } from './runtime.js';

export type Frames = ReturnType<typeof readWire>;
export type Frame = Frames[number];

export function fields(value: unknown): JsonObject {
    return typeof value === 'object' && value !== null && !Array.isArray(value) ? value as JsonObject : {};
}

export function blocks(wire: JsonObject): JsonObject[] {
    const content = fields(wire.message).content;
    return Array.isArray(content) ? content.map(fields) : [];
}

export function root(wire: JsonObject): boolean {
    return wire.parent_tool_use_id == null;
}

export function rootFrames(frames: Frames, direction: string, type: string): Frames {
    return frames.filter(f => f.direction === direction && f.wire.type === type && root(f.wire));
}

export function one(frames: Frames, description: string): Frame {
    if (frames.length !== 1) {
        throw new Untriggered('expected one native ' + description);
    }
    return frames[0];
}

export function control(frames: Frames, direction: string, subtype: string): Frames {
    return frames.filter(f => f.direction === direction && f.wire.type === 'control_request'
        && fields(f.wire.request).subtype === subtype);
}

export function reply(frames: Frames, request: Frame): { index: number; response: JsonObject } {
    const id = request.wire.request_id;
    if (typeof id !== 'string' || !id) {
        throw new Untriggered('missing native control request identity');
    }
    const answer = one(frames.filter(f => f.direction !== request.direction && f.wire.type === 'control_response'
        && fields(f.wire.response).request_id === id), 'control response');
    if (answer.index <= request.index) {
        throw new Untriggered('native control response precedes its request');
    }
    return { index: answer.index, response: fields(answer.wire.response) };
}

export function ordered(...positions: number[]): boolean {
    return positions.every((position, i) => i === 0 || positions[i - 1] < position);
}
