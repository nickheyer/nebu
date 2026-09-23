// Support all gateway protocols so the console can exercise translation.
import { gatewayCredentials } from './auth.svelte';

export type Dialect = 'openai' | 'anthropic' | 'ollama';

export interface ChatMessage {
  role: 'system' | 'user' | 'assistant' | 'tool';
  content: string;
  // Base64 images precede the text.
  images?: ImagePart[];
  toolCalls?: ToolCall[];
  toolCallId?: string;
}

export interface ImagePart {
  mediaType: string;
  data: string;
}

export interface ToolCall {
  id: string;
  name: string;
  arguments: string;
}

export interface Sampling {
  temperature?: number;
  topP?: number;
  topK?: number;
  maxTokens?: number;
  stop?: string[];
  seed?: number;
}

export interface ChatRequest {
  base: string;
  dialect: Dialect;
  model: string;
  messages: ChatMessage[];
  system: string;
  sampling: Sampling;
  stream: boolean;
  tools?: unknown[];
  key?: string;
  signal: AbortSignal;
}

export interface ChatEvent {
  kind: 'text' | 'tool' | 'image' | 'usage' | 'stop' | 'error';
  text?: string;
  tool?: Partial<ToolCall> & { index: number };
  image?: { url: string };
  promptTokens?: number;
  completionTokens?: number;
  stop?: string;
  error?: string;
}

export interface Sent {
  trace: string;
  status: number;
  body: string;
  path: string;
  headers: Record<string, string>;
}

export function render(req: Omit<ChatRequest, 'signal' | 'base' | 'key'>): { path: string; body: Record<string, unknown>; headers: Record<string, string> } {
  const s = req.sampling;
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (req.dialect === 'anthropic') {
    headers['anthropic-version'] = '2023-06-01';
    const body: Record<string, unknown> = {
      model: req.model,
      max_tokens: s.maxTokens || 4096,
      messages: req.messages
        .filter((m) => m.role !== 'system')
        .map((m) => {
          if (m.role === 'tool') return { role: 'user', content: [{ type: 'tool_result', tool_use_id: m.toolCallId, content: m.content }] };
          if (m.role === 'assistant' && m.toolCalls?.length) {
            const blocks: unknown[] = [];
            if (m.content) blocks.push({ type: 'text', text: m.content });
            for (const t of m.toolCalls) blocks.push({ type: 'tool_use', id: t.id, name: t.name, input: parseArgs(t.arguments) });
            return { role: 'assistant', content: blocks };
          }
          if (m.images?.length) {
            const blocks: unknown[] = m.images.map((i) => ({ type: 'image', source: { type: 'base64', media_type: i.mediaType, data: i.data } }));
            if (m.content) blocks.push({ type: 'text', text: m.content });
            return { role: m.role, content: blocks };
          }
          return { role: m.role, content: m.content };
        }),
      stream: req.stream
    };
    if (req.system) body.system = req.system;
    if (s.temperature !== undefined) body.temperature = s.temperature;
    if (s.topP !== undefined) body.top_p = s.topP;
    if (s.topK !== undefined) body.top_k = s.topK;
    if (s.stop?.length) body.stop_sequences = s.stop;
    if (req.tools?.length) body.tools = req.tools.map((t) => anthropicTool(t));
    return { path: '/v1/messages', body, headers };
  }
  const messages = [...(req.system ? [{ role: 'system', content: req.system }] : []), ...req.messages.map(req.dialect === 'ollama' ? ollamaMessage : openaiMessage)];
  if (req.dialect === 'ollama') {
    const options: Record<string, unknown> = {};
    if (s.temperature !== undefined) options.temperature = s.temperature;
    if (s.topP !== undefined) options.top_p = s.topP;
    if (s.topK !== undefined) options.top_k = s.topK;
    if (s.maxTokens) options.num_predict = s.maxTokens;
    if (s.stop?.length) options.stop = s.stop;
    if (s.seed !== undefined) options.seed = s.seed;
    const body: Record<string, unknown> = { model: req.model, messages, stream: req.stream };
    if (Object.keys(options).length) body.options = options;
    if (req.tools?.length) body.tools = req.tools;
    return { path: '/api/chat', body, headers };
  }
  const body: Record<string, unknown> = { model: req.model, messages, stream: req.stream };
  if (req.stream) body.stream_options = { include_usage: true };
  if (s.temperature !== undefined) body.temperature = s.temperature;
  if (s.topP !== undefined) body.top_p = s.topP;
  if (s.maxTokens) body.max_tokens = s.maxTokens;
  if (s.stop?.length) body.stop = s.stop;
  if (s.seed !== undefined) body.seed = s.seed;
  if (req.tools?.length) body.tools = req.tools;
  return { path: '/v1/chat/completions', body, headers };
}

function openaiMessage(m: ChatMessage): Record<string, unknown> {
  // Use a plain string for text-only messages.
  const content = m.images?.length
    ? [...m.images.map((i) => ({ type: 'image_url', image_url: { url: dataUrl(i) } })), ...(m.content ? [{ type: 'text', text: m.content }] : [])]
    : m.content;
  const out: Record<string, unknown> = { role: m.role, content };
  if (m.toolCalls?.length) out.tool_calls = m.toolCalls.map((t) => ({ id: t.id, type: 'function', function: { name: t.name, arguments: t.arguments } }));
  if (m.toolCallId) out.tool_call_id = m.toolCallId;
  return out;
}

// Ollama accepts bare base64 image data.
function ollamaMessage(m: ChatMessage): Record<string, unknown> {
  const out = openaiMessage({ ...m, images: undefined });
  if (m.images?.length) out.images = m.images.map((i) => i.data);
  return out;
}

export function dataUrl(i: ImagePart): string {
  return `data:${i.mediaType};base64,${i.data}`;
}

// Detect the image type from its base64 prefix, defaulting to PNG.
export function sniffBase64(data: string): string {
  if (data.startsWith('/9j/')) return 'image/jpeg';
  if (data.startsWith('R0lGOD')) return 'image/gif';
  if (data.startsWith('UklGR')) return 'image/webp';
  return 'image/png';
}

function anthropicTool(t: unknown): unknown {
  const tool = t as { type?: string; function?: { name: string; description?: string; parameters?: unknown }; name?: string; description?: string; input_schema?: unknown };
  if (tool.function) return { name: tool.function.name, description: tool.function.description, input_schema: tool.function.parameters ?? { type: 'object', properties: {} } };
  return { name: tool.name, description: tool.description, input_schema: tool.input_schema ?? { type: 'object', properties: {} } };
}

function parseArgs(text: string): unknown {
  try {
    return JSON.parse(text || '{}');
  } catch {
    return {};
  }
}

export function errorMessage(raw: string): string {
  try {
    const parsed = JSON.parse(raw);
    if (typeof parsed?.error === 'string') return parsed.error;
    if (parsed?.error?.message) return parsed.error.message;
    if (parsed?.message) return parsed.message;
  } catch {
  }
  return raw;
}

export async function send(req: ChatRequest, emit: (ev: ChatEvent) => void, onSent?: (sent: Sent) => void): Promise<void> {
  const { path, body, headers } = render(req);
  if (req.key) {
    if (req.dialect === 'anthropic') headers['x-api-key'] = req.key;
    else headers.Authorization = 'Bearer ' + req.key;
  }
  const text = JSON.stringify(body);
  const resp = await fetch(req.base + path, { method: 'POST', headers, body: text, signal: req.signal, credentials: gatewayCredentials() });
  onSent?.({ trace: resp.headers.get('X-Nebu-Trace') ?? '', status: resp.status, body: text, path, headers });
  if (!resp.ok || !resp.body) {
    const raw = await resp.text();
    throw new Error(`${resp.status}: ${errorMessage(raw)}`);
  }
  if (!req.stream) {
    const raw = await resp.text();
    readWhole(req.dialect, raw, emit);
    return;
  }
  const reader = resp.body.getReader();
  const decoder = new TextDecoder();
  let pending = '';
  const lines = async function* () {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      pending += decoder.decode(value, { stream: true });
      const parts = pending.split('\n');
      pending = parts.pop() ?? '';
      for (const line of parts) yield line;
    }
    if (pending) yield pending;
  };
  if (req.dialect === 'ollama') {
    for await (const line of lines()) {
      if (!line.trim()) continue;
      readOllama(JSON.parse(line), emit);
    }
    return;
  }
  let event = '';
  for await (const line of lines()) {
    if (line.startsWith('event:')) {
      event = line.slice(6).trim();
      continue;
    }
    const field = line.startsWith('data:') ? 'data' : line.startsWith('error:') ? 'error' : '';
    if (!field) continue;
    const data = line.slice(field.length + 1).trim();
    if (data === '[DONE]') continue;
    let chunk: Record<string, unknown>;
    try {
      chunk = JSON.parse(data);
    } catch {
      if (field === 'error') emit({ kind: 'error', error: data });
      continue;
    }
    if (field === 'error' || (event === 'error' && req.dialect === 'anthropic')) {
      emit({ kind: 'error', error: errorMessage(data) });
      continue;
    }
    if (req.dialect === 'anthropic') readAnthropic(chunk, emit);
    else readOpenai(chunk, emit);
    event = '';
  }
}

function readOpenai(chunk: Record<string, unknown>, emit: (ev: ChatEvent) => void) {
  const error = chunk.error as { message?: string } | undefined;
  if (error) {
    emit({ kind: 'error', error: error.message ?? JSON.stringify(error) });
    return;
  }
  const usage = chunk.usage as { prompt_tokens?: number; completion_tokens?: number } | undefined;
  if (usage) emit({ kind: 'usage', promptTokens: usage.prompt_tokens, completionTokens: usage.completion_tokens });
  type Call = { index?: number; id?: string; function?: { name?: string; arguments?: string } };
  type Content = string | { type: string; text?: string; image_url?: { url?: string } }[] | null;
  const choices = (chunk.choices as { delta?: { content?: Content; tool_calls?: Call[] }; message?: { content?: Content; tool_calls?: Call[] }; text?: string; finish_reason?: string }[]) ?? [];
  for (const c of choices) {
    const content = c.delta?.content ?? c.message?.content ?? c.text ?? '';
    // Handle both plain text and multipart responses.
    if (typeof content === 'string') {
      if (content) emit({ kind: 'text', text: content });
    } else if (Array.isArray(content)) {
      for (const p of content) {
        if (p.type === 'text' && p.text) emit({ kind: 'text', text: p.text });
        else if (p.type === 'image_url' && p.image_url?.url) emit({ kind: 'image', image: { url: p.image_url.url } });
      }
    }
    const calls = c.delta?.tool_calls ?? c.message?.tool_calls ?? [];
    calls.forEach((t, i) => emit({ kind: 'tool', tool: { index: t.index ?? i, id: t.id, name: t.function?.name, arguments: t.function?.arguments } }));
    if (c.finish_reason) emit({ kind: 'stop', stop: c.finish_reason });
  }
}

function readAnthropic(chunk: Record<string, unknown>, emit: (ev: ChatEvent) => void) {
  const type = chunk.type as string;
  const index = (chunk.index as number) ?? 0;
  switch (type) {
    case 'message_start': {
      const usage = (chunk.message as { usage?: { input_tokens?: number } })?.usage;
      if (usage) emit({ kind: 'usage', promptTokens: usage.input_tokens });
      break;
    }
    case 'content_block_start': {
      const block = chunk.content_block as { type: string; id?: string; name?: string };
      if (block?.type === 'tool_use') emit({ kind: 'tool', tool: { index, id: block.id, name: block.name } });
      break;
    }
    case 'content_block_delta': {
      const delta = chunk.delta as { type: string; text?: string; partial_json?: string };
      if (delta?.type === 'text_delta' && delta.text) emit({ kind: 'text', text: delta.text });
      if (delta?.type === 'input_json_delta') emit({ kind: 'tool', tool: { index, arguments: delta.partial_json ?? '' } });
      break;
    }
    case 'message_delta': {
      const delta = chunk.delta as { stop_reason?: string };
      const usage = chunk.usage as { input_tokens?: number; output_tokens?: number } | undefined;
      if (usage) emit({ kind: 'usage', promptTokens: usage.input_tokens, completionTokens: usage.output_tokens });
      if (delta?.stop_reason) emit({ kind: 'stop', stop: delta.stop_reason });
      break;
    }
    case 'error': {
      const err = chunk.error as { message?: string };
      emit({ kind: 'error', error: err?.message ?? 'error' });
      break;
    }
  }
}

function readOllama(chunk: Record<string, unknown>, emit: (ev: ChatEvent) => void) {
  if (typeof chunk.error === 'string') {
    emit({ kind: 'error', error: chunk.error });
    return;
  }
  const msg = chunk.message as { content?: string; images?: string[]; tool_calls?: { function: { name: string; arguments: unknown } }[] } | undefined;
  if (msg?.content) emit({ kind: 'text', text: msg.content });
  for (const data of msg?.images ?? []) emit({ kind: 'image', image: { url: dataUrl({ mediaType: sniffBase64(data), data }) } });
  msg?.tool_calls?.forEach((t, i) => emit({ kind: 'tool', tool: { index: i, id: `call-${i}`, name: t.function.name, arguments: JSON.stringify(t.function.arguments ?? {}) } }));
  if (typeof chunk.response === 'string' && chunk.response) emit({ kind: 'text', text: chunk.response });
  if (chunk.done) {
    emit({ kind: 'usage', promptTokens: chunk.prompt_eval_count as number | undefined, completionTokens: chunk.eval_count as number | undefined });
    emit({ kind: 'stop', stop: (chunk.done_reason as string) || 'stop' });
  }
}

function readWhole(dialect: Dialect, raw: string, emit: (ev: ChatEvent) => void) {
  const parsed = JSON.parse(raw) as Record<string, unknown>;
  if (dialect === 'ollama') {
    readOllama(parsed, emit);
    return;
  }
  if (dialect === 'anthropic') {
    const blocks = (parsed.content as { type: string; text?: string; id?: string; name?: string; input?: unknown; source?: { type: string; media_type?: string; data?: string; url?: string } }[]) ?? [];
    blocks.forEach((b, i) => {
      if (b.type === 'text' && b.text) emit({ kind: 'text', text: b.text });
      if (b.type === 'tool_use') emit({ kind: 'tool', tool: { index: i, id: b.id, name: b.name, arguments: JSON.stringify(b.input ?? {}) } });
      if (b.type === 'image' && b.source) {
        const url = b.source.type === 'url' ? b.source.url : b.source.data ? dataUrl({ mediaType: b.source.media_type || sniffBase64(b.source.data), data: b.source.data }) : '';
        if (url) emit({ kind: 'image', image: { url } });
      }
    });
    const usage = parsed.usage as { input_tokens?: number; output_tokens?: number } | undefined;
    if (usage) emit({ kind: 'usage', promptTokens: usage.input_tokens, completionTokens: usage.output_tokens });
    emit({ kind: 'stop', stop: (parsed.stop_reason as string) || 'end_turn' });
    return;
  }
  readOpenai(parsed, emit);
}

export async function countTokens(base: string, model: string, system: string, messages: ChatMessage[], key: string, signal: AbortSignal): Promise<number> {
  const { path, body, headers } = render({ dialect: 'anthropic', model, messages, system, sampling: {}, stream: false });
  delete body.max_tokens;
  delete body.stream;
  if (key) headers['x-api-key'] = key;
  const resp = await fetch(base + path.replace('/v1/messages', '/v1/messages/count_tokens'), { method: 'POST', headers, body: JSON.stringify(body), signal, credentials: gatewayCredentials() });
  if (!resp.ok) throw new Error(`${resp.status}: ${errorMessage(await resp.text())}`);
  const parsed = (await resp.json()) as { input_tokens?: number };
  return parsed.input_tokens ?? 0;
}
