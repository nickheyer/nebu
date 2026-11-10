import { create, clone } from '@bufbuild/protobuf';
import { timestampNow } from '@bufbuild/protobuf/wkt';
import { chat, images, pollVideo, startVideo, videoContent, type ChatMessage, type ChatParams, type Dialect, type ModelEntry } from '#lib/api/gateway.js';
import { date, num } from '#lib/format.js';
import { isCancel, reason, rpc } from '#lib/api/transport.js';
import { ChatFileSchema, ChatSettingsSchema, ChatTurnSchema, ConversationSchema, type ChatFile, type ChatSettings, type ChatTurn, type Conversation } from '#lib/proto/nebu/v1/chat_pb.js';
import { TraceToolCallSchema, type Trace, type TraceToolCall } from '#lib/proto/nebu/v1/gateway_pb.js';
import type { ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
import type { Option } from '#lib/ui/fields/types.js';
import { toast } from '#lib/shell/notify.svelte.js';

export type Mode = 'text' | 'image' | 'video' | 'other';

export type MediaSettings = { imageSize: string; imageSteps: number; imageCount: number; videoSeconds: number; videoFps: number };

export const imageSizes = ['512x512', '768x768', '1024x1024', '1024x768', '768x1024'];
export const videoFps = [8, 12, 16, 24, 30];

// A tool call waiting for the person to run or skip it
export type PendingCall = { id: string; name: string; arguments: string; server?: ToolServerStatus };

function dialectOf(s: string): Dialect {
	return s === 'anthropic' || s === 'ollama' ? s : 'openai';
}

function numOr(s: string): number | undefined {
	if (s === '') return undefined;
	const n = Number(s);
	return Number.isNaN(n) ? undefined : n;
}

// Reads a file as a data URL
function dataUrl(blob: Blob): Promise<string> {
	return new Promise((resolve, reject) => {
		const r = new FileReader();
		r.onload = () => resolve(String(r.result));
		r.onerror = () => reject(r.error);
		r.readAsDataURL(blob);
	});
}

function bytesOf(url: string): Uint8Array {
	const b64 = url.slice(url.indexOf(',') + 1);
	const bin = atob(b64);
	const out = new Uint8Array(bin.length);
	for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
	return out;
}

// One open conversation, its turns, the request in flight, and the files it shows
export class ChatSession {
	conversation = $state<Conversation>(create(ConversationSchema, { id: crypto.randomUUID(), settings: create(ChatSettingsSchema, { dialect: 'openai', stream: true }) }));
	model = $state('');
	busy = $state(false);
	// Set while a saved conversation is being fetched
	opening = $state(false);
	pending = $state<PendingCall[]>([]);
	// Streamed text of the turn being written
	draft = $state('');
	draftTrace = $state('');
	// Tool calls the gateway ran for the turn being written, from the trace stream
	draftCalls = $state<TraceToolCall[]>([]);
	files = $state<Record<string, string>>({});
	videoProgress = $state<number | null>(null);
	media = $state<MediaSettings>({ imageSize: '1024x1024', imageSteps: 20, imageCount: 1, videoSeconds: 4, videoFps: 16 });
	#abort: AbortController | undefined;
	#saved = $state('');

	get settings(): ChatSettings {
		return this.conversation.settings ?? (this.conversation.settings = create(ChatSettingsSchema, { dialect: 'openai', stream: true }));
	}

	get turns(): ChatTurn[] {
		return this.conversation.turns;
	}

	get isNew(): boolean {
		return this.#saved === '';
	}

	// The id the conversation was last saved under, empty until its first save
	get saved(): string {
		return this.#saved;
	}

	newConversation(): void {
		this.stop();
		const settings = clone(ChatSettingsSchema, this.settings);
		this.conversation = create(ConversationSchema, { id: crypto.randomUUID(), settings });
		this.#saved = '';
		this.pending = [];
	}

	// Loads a saved conversation, the model staying on the current pick
	async open(id: string): Promise<void> {
		this.stop();
		this.opening = true;
		try {
			const res = await rpc.chat.getConversation({ id });
			if (!res.conversation) throw new Error(`conversation ${id} not found`);
			this.conversation = res.conversation;
			if (!this.conversation.settings) this.conversation.settings = create(ChatSettingsSchema, { dialect: 'openai', stream: true });
			this.#saved = id;
			this.pending = [];
			for (const t of this.conversation.turns) for (const f of [...t.images, ...t.media]) void this.loadFile(f);
		} finally {
			this.opening = false;
		}
	}

	async loadFile(f: ChatFile): Promise<void> {
		if (this.files[f.id] || f.url) return;
		try {
			const res = await rpc.chat.getChatFile({ id: f.id });
			const blob = new Blob([new Uint8Array(res.data)], { type: f.mediaType });
			this.files[f.id] = URL.createObjectURL(blob);
		} catch (err) {
			toast.error(`${f.name || 'File'} failed to load`, reason(err));
		}
	}

	fileUrl(f: ChatFile): string {
		return f.url || this.files[f.id] || '';
	}

	async save(): Promise<void> {
		if (this.conversation.turns.length === 0) return;
		this.conversation.model = this.model;
		const res = await rpc.chat.putConversation({ conversation: this.conversation });
		if (res.conversation) {
			this.conversation.title = res.conversation.title;
			this.conversation.updatedAt = res.conversation.updatedAt;
			this.conversation.createdAt = res.conversation.createdAt;
		}
		this.#saved = this.conversation.id;
	}

	// Saves the conversation, a failure raising a toast
	async #persist(): Promise<void> {
		try {
			await this.save();
		} catch (err) {
			toast.error('Conversation failed to save', reason(err));
		}
	}

	stop(): void {
		this.#abort?.abort();
		this.#abort = undefined;
		this.busy = false;
	}

	// The messages the gateway sees for the turns so far, calls no tool turn answers left out
	#messages(): ChatMessage[] {
		const answered = new Set(this.turns.filter((t) => t.role === 'tool').map((t) => t.toolCallId));
		return this.turns.map((t) => {
			const calls = t.toolCalls.filter((c) => answered.has(c.id));
			return {
				role: t.role as ChatMessage['role'],
				text: t.text,
				images: t.images.map((f) => this.fileUrl(f)).filter((u) => u.startsWith('data:') || u.startsWith('blob:')),
				toolCalls: calls.length ? calls.map((c) => ({ id: c.id, name: c.name, arguments: c.arguments })) : undefined,
				toolCallId: t.toolCallId || undefined
			};
		});
	}

	// The request settings, the chosen servers' tools sent as the request's own when each call waits for approval
	#params(servers: ToolServerStatus[]): ChatParams {
		const s = this.settings;
		const approved = s.approveTools ? servers.filter((x) => s.toolServers.includes(x.ref || x.server?.name || '')).flatMap((x) => x.tools.filter((t) => s.mutatingTools || t.readOnly).map((t) => ({ name: t.name, description: t.description, schema: t.schema }))) : [];
		return {
			model: this.model,
			dialect: dialectOf(s.dialect),
			system: s.system,
			stream: s.stream,
			temperature: numOr(s.temperature),
			topP: numOr(s.topP),
			topK: numOr(s.topK),
			maxTokens: numOr(s.maxTokens),
			stop: s.stop ? s.stop.split('\n').filter(Boolean) : [],
			seed: numOr(s.seed),
			tools: approved,
			toolServers: s.approveTools ? [] : s.toolServers,
			mutatingTools: s.mutatingTools
		};
	}

	// Appends a user turn with its images and asks the model for the answer
	async send(text: string, attachments: File[], servers: ToolServerStatus[]): Promise<void> {
		const turn = create(ChatTurnSchema, { role: 'user', text, startedAt: timestampNow(), dialect: this.settings.dialect });
		for (const file of attachments) {
			const url = await dataUrl(file);
			const stored = await rpc.chat.putChatFile({ file: create(ChatFileSchema, { id: crypto.randomUUID(), mediaType: file.type || 'image/png', sizeBytes: BigInt(file.size), name: file.name }), data: bytesOf(url) });
			if (stored.file) {
				turn.images.push(stored.file);
				this.files[stored.file.id] = url;
			}
		}
		this.conversation.turns = [...this.turns, turn];
		await this.answer(servers);
	}

	// Asks the model to continue from the turns so far, running tool rounds until it answers
	async answer(servers: ToolServerStatus[]): Promise<void> {
		const params = this.#params(servers);
		this.busy = true;
		this.draft = '';
		this.draftTrace = '';
		this.draftCalls = [];
		this.pending = [];
		const abort = new AbortController();
		this.#abort = abort;
		const assistant = create(ChatTurnSchema, { role: 'assistant', startedAt: timestampNow(), dialect: params.dialect });
		const conv = this.conversation;
		try {
			const calls: PendingCall[] = [];
			for await (const ev of chat(params, this.#messages(), this.settings.approveTools, abort.signal)) {
				switch (ev.type) {
					case 'trace':
						assistant.traceId = ev.id;
						this.draftTrace = ev.id;
						break;
					case 'text':
						if (!assistant.firstTokenAt) assistant.firstTokenAt = timestampNow();
						this.draft += ev.text;
						break;
					case 'tool_call': {
						const def = this.#serverOf(ev.name, servers);
						calls.push({ id: ev.id, name: ev.name, arguments: ev.arguments, server: def });
						assistant.toolCalls.push(create(TraceToolCallSchema, { id: ev.id, name: ev.name, arguments: ev.arguments, server: def ? def.ref || def.server?.name || '' : '' }));
						break;
					}
					case 'usage':
						if (ev.prompt) assistant.promptTokens = ev.prompt;
						if (ev.completion) assistant.completionTokens = ev.completion;
						break;
					case 'stop':
						assistant.stop = ev.reason;
						break;
				}
			}
			assistant.text = this.draft;
			assistant.finishedAt = timestampNow();
			this.#takeRan(assistant);
			this.conversation.turns = [...this.turns, assistant];
			this.draft = '';
			await this.#persist();
			if (calls.length) this.pending = calls;
		} catch (err) {
			if (this.conversation !== conv) {
				this.draft = '';
				this.draftCalls = [];
				return;
			}
			assistant.text = this.draft;
			assistant.finishedAt = timestampNow();
			this.#takeRan(assistant);
			if (isCancel(err) || abort.signal.aborted) {
				assistant.stop = 'cancelled';
				if (this.draft || assistant.toolCalls.length) this.conversation.turns = [...this.turns, assistant];
			} else {
				assistant.error = reason(err);
				this.conversation.turns = [...this.turns, assistant];
			}
			this.draft = '';
			await this.#persist();
		} finally {
			if (this.#abort === abort) {
				this.#abort = undefined;
				this.busy = false;
			}
		}
	}

	// Moves the calls the gateway ran for the turn being written onto the finished turn
	#takeRan(turn: ChatTurn): void {
		merge(turn, this.draftCalls);
		this.draftCalls = [];
	}

	// Shows the calls the gateway ran for a request on the turn being written or the turn it produced
	observe(trace: Trace): void {
		const ran = trace.toolCalls.filter((c) => c.server);
		if (ran.length === 0) return;
		if (this.busy && trace.id === this.draftTrace) {
			this.draftCalls = ran;
			return;
		}
		const turn = this.turns.find((t) => t.role === 'assistant' && t.traceId === trace.id);
		if (turn && merge(turn, ran)) void this.#persist();
	}

	#serverOf(tool: string, servers: ToolServerStatus[]): ToolServerStatus | undefined {
		return servers.find((s) => s.tools.some((t) => t.name === tool));
	}

	// Runs an approved call through the daemon, appends its tool turn, and continues once every call is settled
	async runCall(call: PendingCall, servers: ToolServerStatus[]): Promise<void> {
		let text = '';
		let failed = false;
		let took = 0;
		const started = Date.now();
		try {
			const res = await rpc.tools.callTool({ name: call.name, arguments: call.arguments, nodeId: call.server?.nodeId ?? '' });
			text = res.parts.map((p) => (p.type === 'image' ? `[image ${p.mediaType}]` : p.text)).join('\n');
			failed = res.failed;
			took = res.tookMs;
		} catch (err) {
			text = reason(err);
			failed = true;
			took = Date.now() - started;
		}
		await this.settle(call, text, failed, servers, took);
	}

	// Records a call's answer as a tool turn and continues once every pending call is settled
	async settle(call: PendingCall, text: string, failed: boolean, servers: ToolServerStatus[], took = 0): Promise<void> {
		const last = this.turns[this.turns.length - 1];
		const record = last?.toolCalls.find((c) => c.id === call.id);
		if (record) {
			record.result = text;
			record.failed = failed;
			record.tookMs = took;
		}
		this.conversation.turns = [...this.turns, create(ChatTurnSchema, { role: 'tool', text, toolCallId: call.id, startedAt: timestampNow(), finishedAt: timestampNow() })];
		this.pending = this.pending.filter((c) => c.id !== call.id);
		if (this.pending.length === 0) await this.answer(servers);
	}

	async skipCall(call: PendingCall, servers: ToolServerStatus[]): Promise<void> {
		await this.settle(call, 'skipped', true, servers);
	}

	// Drops the failed turn, asks again in the route's mode, then removes the files the dropped turn stored
	async retry(servers: ToolServerStatus[], mode: Mode): Promise<void> {
		const last = this.turns[this.turns.length - 1];
		if (last?.role !== 'assistant' || !(last.error || last.stop === 'cancelled')) return;
		this.conversation.turns = this.turns.slice(0, -1);
		const dropped = [...last.images, ...last.media].map((f) => f.id).filter(Boolean);
		const prompt = this.turns[this.turns.length - 1];
		if ((mode === 'image' || mode === 'video') && prompt?.role === 'user') {
			this.conversation.turns = this.turns.slice(0, -1);
			if (mode === 'image') await this.makeImages(prompt.text);
			else await this.makeVideo(prompt.text);
		} else {
			await this.answer(servers);
		}
		if (dropped.length) await rpc.chat.deleteChatFiles({ ids: dropped });
	}

	async makeImages(prompt: string): Promise<void> {
		this.busy = true;
		const abort = new AbortController();
		this.#abort = abort;
		const user = create(ChatTurnSchema, { role: 'user', text: prompt, startedAt: timestampNow() });
		const assistant = create(ChatTurnSchema, { role: 'assistant', startedAt: timestampNow() });
		this.conversation.turns = [...this.turns, user];
		const conv = this.conversation;
		try {
			const res = await images({ model: this.model, prompt, size: this.media.imageSize, steps: this.media.imageSteps, count: this.media.imageCount }, abort.signal);
			assistant.traceId = res.trace;
			for (const [i, url] of res.urls.entries()) {
				const stored = await rpc.chat.putChatFile({ file: create(ChatFileSchema, { id: crypto.randomUUID(), mediaType: 'image/png', name: `image-${i + 1}.png` }), data: bytesOf(url) });
				if (stored.file) {
					assistant.media.push(stored.file);
					this.files[stored.file.id] = url;
				}
			}
			assistant.stop = 'stop';
		} catch (err) {
			if (isCancel(err) || abort.signal.aborted) assistant.stop = 'cancelled';
			else assistant.error = reason(err);
		} finally {
			assistant.finishedAt = timestampNow();
			if (this.conversation === conv) {
				this.conversation.turns = [...this.turns, assistant];
				await this.#persist();
			}
			if (this.#abort === abort) {
				this.#abort = undefined;
				this.busy = false;
			}
		}
	}

	async makeVideo(prompt: string): Promise<void> {
		this.busy = true;
		this.videoProgress = 0;
		const abort = new AbortController();
		this.#abort = abort;
		const user = create(ChatTurnSchema, { role: 'user', text: prompt, startedAt: timestampNow() });
		const assistant = create(ChatTurnSchema, { role: 'assistant', startedAt: timestampNow() });
		this.conversation.turns = [...this.turns, user];
		const conv = this.conversation;
		try {
			let job = await startVideo({ model: this.model, prompt, size: this.media.imageSize, seconds: this.media.videoSeconds, fps: this.media.videoFps, steps: this.media.imageSteps }, abort.signal);
			assistant.traceId = job.trace ?? '';
			while (job.status !== 'completed' && job.status !== 'failed') {
				await new Promise((r) => setTimeout(r, 1500));
				if (abort.signal.aborted) throw new DOMException('aborted', 'AbortError');
				job = await pollVideo(job.id, abort.signal);
				this.videoProgress = job.progress ?? null;
			}
			if (job.status === 'failed') throw new Error(job.error?.message ?? 'video failed');
			const blob = await videoContent(job.id, abort.signal);
			const url = await dataUrl(blob);
			const stored = await rpc.chat.putChatFile({ file: create(ChatFileSchema, { id: crypto.randomUUID(), mediaType: blob.type || 'video/mp4', name: `${job.id}.${job.output_format ?? 'mp4'}`, sizeBytes: BigInt(blob.size) }), data: bytesOf(url) });
			if (stored.file) {
				assistant.media.push(stored.file);
				this.files[stored.file.id] = URL.createObjectURL(blob);
			}
			assistant.stop = 'stop';
		} catch (err) {
			if (isCancel(err) || abort.signal.aborted) assistant.stop = 'cancelled';
			else assistant.error = reason(err);
		} finally {
			assistant.finishedAt = timestampNow();
			this.videoProgress = null;
			if (this.conversation === conv) {
				this.conversation.turns = [...this.turns, assistant];
				await this.#persist();
			}
			if (this.#abort === abort) {
				this.#abort = undefined;
				this.busy = false;
			}
		}
	}
}

// Adds or refreshes calls on a turn by id, answering whether anything changed
function merge(turn: ChatTurn, calls: TraceToolCall[]): boolean {
	let changed = false;
	for (const c of calls) {
		const have = turn.toolCalls.find((x) => x.id === c.id);
		if (!have) {
			turn.toolCalls.push(create(TraceToolCallSchema, c));
			changed = true;
		} else if (have.result !== c.result || have.failed !== c.failed || have.tookMs !== c.tookMs) {
			have.result = c.result;
			have.failed = c.failed;
			have.tookMs = c.tookMs;
			changed = true;
		}
	}
	return changed;
}

// Whether a gateway model takes images, makes images, or makes videos
export function modelMode(m: ModelEntry | undefined): Mode {
	if (!m) return 'text';
	if (m.capabilities.includes('videos')) return 'video';
	if (m.capabilities.includes('images')) return 'image';
	if (m.capabilities.includes('completion')) return 'text';
	return 'other';
}

export function takesImages(m: ModelEntry | undefined): boolean {
	return (m?.inventory.modalities ?? []).includes('image') && modelMode(m) === 'text';
}

// Ready routes holding every typed word, text routes first, context and quantization in each tooltip
export function modelOptions(models: ModelEntry[], query: string): Option<string>[] {
	const words = query.toLowerCase().split(/\s+/).filter(Boolean);
	const ready = models.filter((m) => m.ready && words.every((w) => m.id.toLowerCase().includes(w)));
	const rank = (m: ModelEntry) => (modelMode(m) === 'text' ? 0 : 1);
	return [...ready]
		.sort((a, b) => rank(a) - rank(b) || a.id.localeCompare(b.id))
		.map((m) => {
			const inv = m.inventory;
			const facts = [inv.context_length ? `${num(inv.context_length)} context` : '', inv.quantization, modelMode(m) === 'text' ? '' : modelMode(m)].filter(Boolean);
			return { value: m.id, label: m.id, tip: facts.join('\n') || undefined };
		});
}

export type DayGroup = { label: string; items: Conversation[] };

// Conversations in their listed order under Today, Yesterday, or their date
export function byDay(list: Conversation[], now = new Date()): DayGroup[] {
	const today = now.toDateString();
	const yesterday = new Date(now.getTime() - 86_400_000).toDateString();
	const groups: DayGroup[] = [];
	for (const c of list) {
		const d = date(c.updatedAt) ?? date(c.createdAt);
		const day = d?.toDateString() ?? '';
		const label = !d ? 'Undated' : day === today ? 'Today' : day === yesterday ? 'Yesterday' : d.toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short', year: d.getFullYear() === now.getFullYear() ? undefined : 'numeric' });
		const last = groups[groups.length - 1];
		if (last?.label === label) last.items.push(c);
		else groups.push({ label, items: [c] });
	}
	return groups;
}

// Opens the browser's file chooser for images and answers the files picked
export function chooseImages(): Promise<File[]> {
	return new Promise((resolve) => {
		const chooser = document.createElement('input');
		chooser.type = 'file';
		chooser.accept = 'image/*';
		chooser.multiple = true;
		chooser.addEventListener('change', () => resolve(Array.from(chooser.files ?? [])));
		chooser.addEventListener('cancel', () => resolve([]));
		chooser.click();
	});
}
