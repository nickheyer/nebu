// Nebu's own MCP server at /mcp, as the signed in person: the tools it lists and one call of a tool
import { failure, GatewayError, sse } from '#lib/api/gateway.js';

export type McpTool = { name: string; description: string; schema: string; readOnly: boolean; destructive: boolean };

// What the server tells every client at initialize, and its tools
export type McpServer = { instructions: string; tools: McpTool[] };

export type McpResult = { text: string; failed: boolean };

type Json = Record<string, unknown>;

// The protocol version asked for, the daemon answering with the one it settles on
const asked = '2025-06-18';

let seq = 0;
// The session the daemon gave at initialize, sent with every request after it
let session: { id: string; version: string; instructions: string } | undefined;
let opening: Promise<void> | undefined;

function post(body: Json, signal: AbortSignal): Promise<Response> {
	const headers: Record<string, string> = { 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream' };
	if (session) {
		headers['Mcp-Session-Id'] = session.id;
		headers['MCP-Protocol-Version'] = session.version;
	}
	return fetch('/mcp', { method: 'POST', headers, body: JSON.stringify({ jsonrpc: '2.0', ...body }), signal, credentials: 'same-origin' });
}

// The result of the request with this id, from a JSON body or from the event stream the daemon answers with
async function result(res: Response, id: number, signal: AbortSignal): Promise<Json> {
	if (!res.ok) throw await failure(res);
	const answers = async function* (): AsyncGenerator<Json> {
		if (!(res.headers.get('Content-Type') ?? '').startsWith('text/event-stream')) yield (await res.json()) as Json;
		else for await (const ev of sse(res, signal)) yield JSON.parse(ev.data) as Json;
	};
	for await (const doc of answers()) {
		if (doc['id'] !== id) continue;
		const error = doc['error'] as Json | undefined;
		if (error) throw new GatewayError(res.status, String(error['message'] ?? 'the request failed'));
		return (doc['result'] as Json | undefined) ?? {};
	}
	throw new GatewayError(res.status, 'the daemon closed the connection before responding');
}

// Starts a session unless one is open or opening
function open(signal: AbortSignal): Promise<void> {
	if (session) return Promise.resolve();
	return (opening ??= initialize(signal).finally(() => (opening = undefined)));
}

async function initialize(signal: AbortSignal): Promise<void> {
	const id = ++seq;
	const res = await post({ id, method: 'initialize', params: { protocolVersion: asked, capabilities: {}, clientInfo: { name: 'nebu-web', version: '1' } } }, signal);
	const init = await result(res, id, signal);
	session = { id: res.headers.get('Mcp-Session-Id') ?? '', version: String(init['protocolVersion'] ?? asked), instructions: String(init['instructions'] ?? '') };
	await post({ method: 'notifications/initialized' }, signal);
}

// Ends the session when the page goes, the daemon keeping one until it is told otherwise
if (typeof window !== 'undefined') {
	window.addEventListener('pagehide', () => {
		if (!session) return;
		void fetch('/mcp', { method: 'DELETE', headers: { 'Mcp-Session-Id': session.id }, keepalive: true, credentials: 'same-origin' });
		session = undefined;
	});
}

// Sends one request in the open session, starting a new session once when the daemon has forgotten the old one
async function request(method: string, params: Json, signal = new AbortController().signal): Promise<Json> {
	for (let again = false; ; again = true) {
		await open(signal);
		const id = ++seq;
		const sent = session;
		const res = await post({ id, method, params }, signal);
		if (res.status === 404 && !again) {
			if (session === sent) session = undefined;
			continue;
		}
		return result(res, id, signal);
	}
}

// Lists every tool of the server, with what it tells its clients at initialize
export async function connect(signal?: AbortSignal): Promise<McpServer> {
	const tools: McpTool[] = [];
	for (let cursor = ''; ; ) {
		const page = await request('tools/list', cursor ? { cursor } : {}, signal);
		for (const t of (page['tools'] as Json[] | undefined) ?? []) {
			const hints = (t['annotations'] as Json | undefined) ?? {};
			tools.push({ name: String(t['name']), description: String(t['description'] ?? ''), schema: JSON.stringify(t['inputSchema'] ?? { type: 'object', properties: {} }), readOnly: hints['readOnlyHint'] === true, destructive: hints['destructiveHint'] === true });
		}
		cursor = String(page['nextCursor'] ?? '');
		if (!cursor) return { instructions: session?.instructions ?? '', tools };
	}
}

// Runs one tool with its arguments as the model wrote them, answering its text and whether the tool reported a failure
export async function call(name: string, args: string, signal?: AbortSignal): Promise<McpResult> {
	const res = await request('tools/call', { name, arguments: JSON.parse(args || '{}') as Json }, signal);
	const content = (res['content'] as Json[] | undefined) ?? [];
	return { text: content.map((c) => (c['type'] === 'text' ? String(c['text'] ?? '') : `[${String(c['type'])} ${String(c['mimeType'] ?? '')}]`)).join('\n'), failed: res['isError'] === true };
}
