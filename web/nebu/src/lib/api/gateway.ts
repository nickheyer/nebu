// The gateway's HTTP surface the chat page talks to, chat completions in three dialects, images, videos and models

export type Dialect = 'openai' | 'anthropic' | 'ollama';

export type ToolDef = { name: string; description: string; schema: string };

export type ChatMessage = {
	role: 'system' | 'user' | 'assistant' | 'tool';
	text: string;
	// Data URLs of images attached to a user turn
	images?: string[];
	toolCalls?: { id: string; name: string; arguments: string }[];
	toolCallId?: string;
};

export type ChatParams = {
	model: string;
	dialect: Dialect;
	system: string;
	stream: boolean;
	temperature?: number;
	topP?: number;
	topK?: number;
	maxTokens?: number;
	stop: string[];
	seed?: number;
	tools: ToolDef[];
	// Tool servers the gateway runs itself, by name
	toolServers: string[];
	mutatingTools: boolean;
};

export type StreamEvent =
	| { type: 'text'; text: string }
	| { type: 'tool_call'; index: number; id: string; name: string; arguments: string }
	| { type: 'usage'; prompt: number; completion: number }
	| { type: 'stop'; reason: string }
	| { type: 'trace'; id: string };

export type ModelEntry = { id: string; ready: boolean; capabilities: string[]; inventory: { context_length: number; quantization: string; accelerator: string; modalities: string[]; throughput_class: string } };

export class GatewayError extends Error {
	constructor(
		public status: number,
		message: string
	) {
		super(message);
	}
}

async function failure(res: Response): Promise<GatewayError> {
	const text = await res.text();
	try {
		const body = JSON.parse(text) as { error?: { message?: string } | string };
		const msg = typeof body.error === 'string' ? body.error : body.error?.message;
		return new GatewayError(res.status, msg || text || res.statusText);
	} catch {
		return new GatewayError(res.status, text || res.statusText);
	}
}

export async function listModels(signal?: AbortSignal): Promise<ModelEntry[]> {
	const res = await fetch('/v1/models', { signal, credentials: 'same-origin' });
	if (!res.ok) throw await failure(res);
	const body = (await res.json()) as { data: ModelEntry[] };
	return body.data;
}

function parseSchema(s: string): unknown {
	try {
		return s ? JSON.parse(s) : { type: 'object', properties: {} };
	} catch {
		return { type: 'object', properties: {} };
	}
}

function dataUrlParts(url: string): { mediaType: string; data: string } {
	const m = /^data:([^;]+);base64,(.*)$/.exec(url);
	return { mediaType: m?.[1] ?? 'image/png', data: m?.[2] ?? '' };
}

// The request body one dialect takes for the conversation so far
function body(p: ChatParams, messages: ChatMessage[]): { path: string; body: Record<string, unknown> } {
	switch (p.dialect) {
		case 'anthropic': {
			const msgs = messages
				.filter((m) => m.role !== 'system')
				.map((m) => {
					if (m.role === 'tool') return { role: 'user', content: [{ type: 'tool_result', tool_use_id: m.toolCallId, content: m.text }] };
					if (m.role === 'assistant' && m.toolCalls?.length) {
						const content: unknown[] = m.text ? [{ type: 'text', text: m.text }] : [];
						for (const c of m.toolCalls) content.push({ type: 'tool_use', id: c.id, name: c.name, input: parseSchema(c.arguments) });
						return { role: 'assistant', content };
					}
					if (m.images?.length) {
						return { role: m.role, content: [...m.images.map((u) => ({ type: 'image', source: { type: 'base64', ...dataUrlParts(u) } })), { type: 'text', text: m.text }] };
					}
					return { role: m.role, content: m.text };
				});
			return {
				path: '/v1/messages',
				body: {
					model: p.model,
					system: p.system || undefined,
					messages: msgs,
					max_tokens: p.maxTokens ?? 4096,
					stream: p.stream,
					temperature: p.temperature,
					top_p: p.topP,
					top_k: p.topK,
					stop_sequences: p.stop.length ? p.stop : undefined,
					tools: p.tools.length ? p.tools.map((t) => ({ name: t.name, description: t.description, input_schema: parseSchema(t.schema) })) : undefined
				}
			};
		}
		case 'ollama': {
			const msgs = [...(p.system ? [{ role: 'system', content: p.system }] : []), ...messages.map((m) => ({ role: m.role, content: m.text, images: m.images?.map((u) => dataUrlParts(u).data), tool_calls: m.toolCalls?.map((c) => ({ function: { name: c.name, arguments: parseSchema(c.arguments) } })) }))];
			return {
				path: '/api/chat',
				body: {
					model: p.model,
					messages: msgs,
					stream: p.stream,
					options: { temperature: p.temperature, top_p: p.topP, top_k: p.topK, num_predict: p.maxTokens, stop: p.stop.length ? p.stop : undefined, seed: p.seed },
					tools: p.tools.length ? p.tools.map((t) => ({ type: 'function', function: { name: t.name, description: t.description, parameters: parseSchema(t.schema) } })) : undefined
				}
			};
		}
		default: {
			const msgs = [...(p.system ? [{ role: 'system', content: p.system }] : []), ...messages.map((m) => {
				if (m.role === 'tool') return { role: 'tool', tool_call_id: m.toolCallId, content: m.text };
				if (m.role === 'assistant' && m.toolCalls?.length) return { role: 'assistant', content: m.text || null, tool_calls: m.toolCalls.map((c) => ({ id: c.id, type: 'function', function: { name: c.name, arguments: c.arguments } })) };
				if (m.images?.length) return { role: m.role, content: [{ type: 'text', text: m.text }, ...m.images.map((u) => ({ type: 'image_url', image_url: { url: u } }))] };
				return { role: m.role, content: m.text };
			})];
			return {
				path: '/v1/chat/completions',
				body: {
					model: p.model,
					messages: msgs,
					stream: p.stream,
					stream_options: p.stream ? { include_usage: true } : undefined,
					temperature: p.temperature,
					top_p: p.topP,
					top_k: p.topK,
					max_tokens: p.maxTokens,
					stop: p.stop.length ? p.stop : undefined,
					seed: p.seed,
					tools: p.tools.length ? p.tools.map((t) => ({ type: 'function', function: { name: t.name, description: t.description, parameters: parseSchema(t.schema) } })) : undefined
				}
			};
		}
	}
}

function headers(p: ChatParams, approve: boolean): Record<string, string> {
	const h: Record<string, string> = { 'Content-Type': 'application/json' };
	if (p.toolServers.length) h['X-Nebu-Tools'] = p.toolServers.join(',');
	if (p.mutatingTools) h['X-Nebu-Tools-Mutating'] = 'true';
	if (approve) h['X-Nebu-Approve-Tools'] = 'all';
	return h;
}

async function* lines(res: Response, signal: AbortSignal): AsyncGenerator<string> {
	const reader = res.body?.getReader();
	if (!reader) return;
	const decoder = new TextDecoder();
	let buffer = '';
	while (!signal.aborted) {
		const { done, value } = await reader.read();
		if (done) break;
		buffer += decoder.decode(value, { stream: true });
		let i: number;
		while ((i = buffer.indexOf('\n')) >= 0) {
			yield buffer.slice(0, i).replace(/\r$/, '');
			buffer = buffer.slice(i + 1);
		}
	}
	if (buffer) yield buffer;
}

async function* sse(res: Response, signal: AbortSignal): AsyncGenerator<{ event: string; data: string }> {
	let event = '';
	let data: string[] = [];
	for await (const line of lines(res, signal)) {
		if (line === '') {
			if (data.length) yield { event, data: data.join('\n') };
			event = '';
			data = [];
			continue;
		}
		if (line.startsWith('event:')) event = line.slice(6).trim();
		else if (line.startsWith('data:')) data.push(line.slice(5).trimStart());
	}
	if (data.length) yield { event, data: data.join('\n') };
}

type Json = Record<string, unknown>;

function num(v: unknown): number {
	return typeof v === 'number' ? v : 0;
}

// Runs one chat round against the gateway, yielding text, tool calls, usage and the stop reason as they arrive
export async function* chat(p: ChatParams, messages: ChatMessage[], approve: boolean, signal: AbortSignal): AsyncGenerator<StreamEvent> {
	const { path, body: payload } = body(p, messages);
	const res = await fetch(path, { method: 'POST', headers: headers(p, approve), body: JSON.stringify(payload), signal, credentials: 'same-origin' });
	const trace = res.headers.get('X-Nebu-Trace');
	if (trace) yield { type: 'trace', id: trace };
	if (!res.ok) throw await failure(res);
	if (!p.stream) {
		const doc = (await res.json()) as Json;
		yield* whole(p.dialect, doc);
		return;
	}
	switch (p.dialect) {
		case 'anthropic': {
			const blocks = new Map<number, { id: string; name: string; json: string }>();
			for await (const ev of sse(res, signal)) {
				const doc = JSON.parse(ev.data) as Json;
				const type = doc['type'] as string;
				if (type === 'message_start') {
					const usage = (doc['message'] as Json)?.['usage'] as Json | undefined;
					if (usage) yield { type: 'usage', prompt: num(usage['input_tokens']), completion: 0 };
				} else if (type === 'content_block_start') {
					const block = doc['content_block'] as Json;
					if (block['type'] === 'tool_use') blocks.set(num(doc['index']), { id: String(block['id']), name: String(block['name']), json: '' });
				} else if (type === 'content_block_delta') {
					const delta = doc['delta'] as Json;
					if (delta['type'] === 'text_delta') yield { type: 'text', text: String(delta['text'] ?? '') };
					else if (delta['type'] === 'input_json_delta') {
						const b = blocks.get(num(doc['index']));
						if (b) b.json += String(delta['partial_json'] ?? '');
					}
				} else if (type === 'content_block_stop') {
					const b = blocks.get(num(doc['index']));
					if (b) yield { type: 'tool_call', index: num(doc['index']), id: b.id, name: b.name, arguments: b.json || '{}' };
				} else if (type === 'message_delta') {
					const usage = doc['usage'] as Json | undefined;
					const delta = doc['delta'] as Json | undefined;
					if (usage) yield { type: 'usage', prompt: 0, completion: num(usage['output_tokens']) };
					if (delta?.['stop_reason']) yield { type: 'stop', reason: String(delta['stop_reason']) };
				} else if (type === 'error') {
					throw new GatewayError(500, String((doc['error'] as Json)?.['message'] ?? 'stream error'));
				}
			}
			return;
		}
		case 'ollama': {
			for await (const line of lines(res, signal)) {
				if (!line.trim()) continue;
				const doc = JSON.parse(line) as Json;
				if (doc['error']) throw new GatewayError(500, String(doc['error']));
				const msg = doc['message'] as Json | undefined;
				if (msg?.['content']) yield { type: 'text', text: String(msg['content']) };
				const calls = (msg?.['tool_calls'] as Json[] | undefined) ?? [];
				for (const [i, c] of calls.entries()) {
					const fn = c['function'] as Json;
					yield { type: 'tool_call', index: i, id: `call_${Date.now()}_${i}`, name: String(fn['name']), arguments: JSON.stringify(fn['arguments'] ?? {}) };
				}
				if (doc['done']) {
					yield { type: 'usage', prompt: num(doc['prompt_eval_count']), completion: num(doc['eval_count']) };
					yield { type: 'stop', reason: String(doc['done_reason'] ?? 'stop') };
				}
			}
			return;
		}
		default: {
			const calls = new Map<number, { id: string; name: string; arguments: string }>();
			for await (const ev of sse(res, signal)) {
				if (ev.data === '[DONE]') break;
				const doc = JSON.parse(ev.data) as Json;
				if (doc['error']) throw new GatewayError(500, String((doc['error'] as Json)['message'] ?? doc['error']));
				const choice = ((doc['choices'] as Json[] | undefined) ?? [])[0];
				const delta = choice?.['delta'] as Json | undefined;
				if (delta?.['content']) yield { type: 'text', text: String(delta['content']) };
				for (const tc of (delta?.['tool_calls'] as Json[] | undefined) ?? []) {
					const index = num(tc['index']);
					const fn = tc['function'] as Json | undefined;
					const cur = calls.get(index) ?? { id: '', name: '', arguments: '' };
					if (tc['id']) cur.id = String(tc['id']);
					if (fn?.['name']) cur.name += String(fn['name']);
					if (fn?.['arguments']) cur.arguments += String(fn['arguments']);
					calls.set(index, cur);
				}
				const finish = choice?.['finish_reason'];
				if (finish) {
					for (const [index, c] of calls) yield { type: 'tool_call', index, id: c.id || `call_${index}`, name: c.name, arguments: c.arguments || '{}' };
					calls.clear();
					yield { type: 'stop', reason: String(finish) };
				}
				const usage = doc['usage'] as Json | undefined;
				if (usage) yield { type: 'usage', prompt: num(usage['prompt_tokens']), completion: num(usage['completion_tokens']) };
			}
			return;
		}
	}
}

// The events of a whole answer the gateway sent at once
function* whole(dialect: Dialect, doc: Json): Generator<StreamEvent> {
	if (doc['error']) throw new GatewayError(500, String((doc['error'] as Json)['message'] ?? doc['error']));
	switch (dialect) {
		case 'anthropic': {
			for (const [i, block] of ((doc['content'] as Json[] | undefined) ?? []).entries()) {
				if (block['type'] === 'text') yield { type: 'text', text: String(block['text'] ?? '') };
				if (block['type'] === 'tool_use') yield { type: 'tool_call', index: i, id: String(block['id']), name: String(block['name']), arguments: JSON.stringify(block['input'] ?? {}) };
			}
			const usage = doc['usage'] as Json | undefined;
			if (usage) yield { type: 'usage', prompt: num(usage['input_tokens']), completion: num(usage['output_tokens']) };
			yield { type: 'stop', reason: String(doc['stop_reason'] ?? 'end_turn') };
			return;
		}
		case 'ollama': {
			const msg = doc['message'] as Json | undefined;
			if (msg?.['content']) yield { type: 'text', text: String(msg['content']) };
			for (const [i, c] of (((msg?.['tool_calls'] as Json[] | undefined) ?? []) as Json[]).entries()) {
				const fn = c['function'] as Json;
				yield { type: 'tool_call', index: i, id: `call_${Date.now()}_${i}`, name: String(fn['name']), arguments: JSON.stringify(fn['arguments'] ?? {}) };
			}
			yield { type: 'usage', prompt: num(doc['prompt_eval_count']), completion: num(doc['eval_count']) };
			yield { type: 'stop', reason: String(doc['done_reason'] ?? 'stop') };
			return;
		}
		default: {
			const choice = ((doc['choices'] as Json[] | undefined) ?? [])[0];
			const msg = choice?.['message'] as Json | undefined;
			if (msg?.['content']) yield { type: 'text', text: String(msg['content']) };
			for (const [i, tc] of ((msg?.['tool_calls'] as Json[] | undefined) ?? []).entries()) {
				const fn = tc['function'] as Json;
				yield { type: 'tool_call', index: i, id: String(tc['id'] ?? `call_${i}`), name: String(fn['name']), arguments: String(fn['arguments'] ?? '{}') };
			}
			const usage = doc['usage'] as Json | undefined;
			if (usage) yield { type: 'usage', prompt: num(usage['prompt_tokens']), completion: num(usage['completion_tokens']) };
			yield { type: 'stop', reason: String(choice?.['finish_reason'] ?? 'stop') };
		}
	}
}

export type ImageRequest = { model: string; prompt: string; size: string; steps: number; count: number; negativePrompt?: string };

// Makes images, answering their data URLs and the trace id
export async function images(req: ImageRequest, signal: AbortSignal): Promise<{ urls: string[]; trace: string }> {
	const res = await fetch('/v1/images/generations', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ model: req.model, prompt: req.prompt, n: req.count, size: req.size, steps: req.steps, negative_prompt: req.negativePrompt || undefined, response_format: 'b64_json' }),
		signal,
		credentials: 'same-origin'
	});
	if (!res.ok) throw await failure(res);
	const doc = (await res.json()) as { data: { b64_json: string }[]; output_format?: string };
	const mime = doc.output_format === 'jpeg' ? 'image/jpeg' : doc.output_format === 'webp' ? 'image/webp' : 'image/png';
	return { urls: doc.data.map((d) => `data:${mime};base64,${d.b64_json}`), trace: res.headers.get('X-Nebu-Trace') ?? '' };
}

export type VideoRequest = { model: string; prompt: string; size: string; seconds: number; fps: number; steps: number; negativePrompt?: string };
export type VideoJob = { id: string; status: string; progress: number | null; error?: { message: string } | null; mime_type?: string; output_format?: string; trace?: string };

// Starts a video and answers the job, which the caller polls
export async function startVideo(req: VideoRequest, signal: AbortSignal): Promise<VideoJob> {
	const res = await fetch('/v1/videos', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ model: req.model, prompt: req.prompt, size: req.size, frames: Math.round(req.seconds * req.fps), fps: req.fps, steps: req.steps, negative_prompt: req.negativePrompt || undefined }),
		signal,
		credentials: 'same-origin'
	});
	if (!res.ok) throw await failure(res);
	const job = (await res.json()) as VideoJob;
	job.trace = res.headers.get('X-Nebu-Trace') ?? job.trace;
	return job;
}

export async function pollVideo(id: string, signal: AbortSignal): Promise<VideoJob> {
	const res = await fetch(`/v1/videos/${encodeURIComponent(id)}`, { signal, credentials: 'same-origin' });
	if (!res.ok) throw await failure(res);
	return (await res.json()) as VideoJob;
}

export async function videoContent(id: string, signal: AbortSignal): Promise<Blob> {
	const res = await fetch(`/v1/videos/${encodeURIComponent(id)}/content`, { signal, credentials: 'same-origin' });
	if (!res.ok) throw await failure(res);
	return res.blob();
}
