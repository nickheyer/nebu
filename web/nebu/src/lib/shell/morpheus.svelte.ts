import { create, fromJson, toJson, type JsonValue } from '@bufbuild/protobuf';
import { timestampNow } from '@bufbuild/protobuf/wkt';
import { untrack } from 'svelte';
import { page } from '$app/state';
import { listModels, type ModelEntry } from '#lib/api/gateway.js';
import { call, connect, type McpTool } from '#lib/api/mcp.js';
import { reason } from '#lib/api/transport.js';
import { session as account } from '#lib/auth/session.svelte.js';
import { enumLabel, plural } from '#lib/format.js';
import { live } from '#lib/live/events.svelte.js';
import { ChatSession, modelMode, type PendingCall } from '#lib/pages/chat/chat.svelte.js';
import { ChatTurnSchema, ConversationSchema, type ChatTurn } from '#lib/proto/nebu/v1/chat_pb.js';
import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
import { RouteState } from '#lib/proto/nebu/v1/gateway_pb.js';
import { InstanceState, InstanceStateSchema } from '#lib/proto/nebu/v1/instance_pb.js';
import { ToolServerKind } from '#lib/proto/nebu/v1/settings_pb.js';
import { SlotStateSchema } from '#lib/proto/nebu/v1/slot_pb.js';
import { TaskState, TaskStateSchema, type Task } from '#lib/proto/nebu/v1/task_pb.js';
import { ToolInfoSchema, ToolServerState, ToolServerStatusSchema, type ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
import { chrome } from '#lib/shell/chrome.svelte.js';
import { pageOf } from '#lib/shell/nav.js';
import { prefs } from '#lib/state/prefs.svelte.js';

// The name nebu's own tools go by among the tool servers
const own = 'nebu';
// Where the thread is kept for the tab
const kept = 'nebu.morpheus';
// Names of one kind the context lists at most
const most = 20;
// What the model is told before the lines about the open page
const lead = [
	"You are Morpheus, the assistant built into nebu's web UI. You act on behalf of the signed-in user through these tools. The lines below describe what they are looking at in their browser right now. Use the tools to look up anything else.",
	"When a tool starts a task, you will be told when it finishes, so don't poll it with get_task."
].join('\n');

// The asks offered on an empty thread, by the route of the open page
const asks: Record<string, string[]> = {
	'/discover/[source]/[repo]': ['Set up this model', 'Which weights fit this host?'],
	'/models/[source]/[repo]/[[group]]': ['Run this with the best settings'],
	'/runtimes/[[id]]/[[tab]]': ['Install the right build for this host']
};

// The tools /mcp lists and what it tells its clients at initialize
let tools = $state<McpTool[]>([]);
let instructions = $state('');
let models = $state<ModelEntry[]>([]);
// Set once the tools and the routes have been asked for, whatever came back
let loaded = $state(false);
let failure = $state('');
let loading: Promise<void> | undefined;
// What the message box holds
let text = $state('');
// Tasks a tool answer named that have not ended
let following = $state<string[]>([]);
// The turns the panel sent when a followed task ended, by id, with the state it ended in
let notes = $state<Record<string, TaskState>>({});
// The account the kept thread belongs to, another one in the same tab starting without it
let owner = $state('');

const session = new ChatSession({ kept: false, runs, auto });

// Nebu's own tools as one more tool server, then every ready search and fetch server
const servers = $derived<ToolServerStatus[]>([
	create(ToolServerStatusSchema, { ref: own, state: ToolServerState.READY, tools: tools.map((t) => create(ToolInfoSchema, { name: t.name, description: t.description, schema: t.schema, readOnly: t.readOnly })) }),
	...[...live.toolServers.values()].filter((s) => s.state === ToolServerState.READY && (s.server?.kind === ToolServerKind.SEARCH || s.server?.kind === ToolServerKind.FETCH))
]);

// Runs a call of nebu's own tools through /mcp and follows the task its answer names, leaving any other call to the daemon's tool servers
async function runs(c: PendingCall, signal?: AbortSignal): Promise<{ text: string; failed: boolean } | undefined> {
	await loading;
	if (!tools.some((t) => t.name === c.name)) return undefined;
	const res = await call(c.name, c.arguments, signal);
	if (!res.failed) follow(res.text);
	return res;
}

// Whether a call runs without waiting: one that only reads, and any other once the person is no longer asked, but for one that destroys
function auto(c: PendingCall): boolean {
	const tool = tools.find((t) => t.name === c.name);
	if (tool) return tool.readOnly || (!prefs.morpheusAsks && !tool.destructive);
	const info = c.server?.tools.find((t) => t.name === c.name);
	return info !== undefined && (info.readOnly || !prefs.morpheusAsks);
}

function ended(state: TaskState | undefined): boolean {
	return state === TaskState.SUCCEEDED || state === TaskState.FAILED || state === TaskState.CANCELED;
}

// Follows the task a tool answer names while it is still under way
function follow(answer: string): void {
	try {
		const task = (JSON.parse(answer) as { task?: { id?: string; state?: string } }).task;
		if (task?.id && (task.state === 'TASK_STATE_PENDING' || task.state === 'TASK_STATE_RUNNING') && !following.includes(task.id)) following = [...following, task.id];
	} catch {
		return;
	}
}

// Where the open page sits and its title, as the top bar reads them
function looking(): string {
	const head = chrome.header;
	const from = head?.back?.label ?? pageOf(page.url.pathname)?.label ?? '';
	return [from, head?.title === from ? '' : head?.title].filter(Boolean).join(' / ');
}

function line(label: string, names: string[]): string {
	return names.length ? `${label}: ${names.slice(0, most).join(', ')}` : '';
}

// The open page and the state of the daemon as the shell holds them, names and states only
function context(): string {
	const head = chrome.header;
	const tab = head?.tabs?.find((t) => t.id === head.active)?.label;
	const params = Object.entries(page.params)
		.filter(([, v]) => v)
		.map(([k, v]) => `${k}=${v}`)
		.join(' ');
	const host = live.settings?.hostLabel || live.host?.hostname || '';
	const lines = [
		`Page: ${looking()}${tab ? `, tab ${tab}` : ''}`,
		`Address: ${page.url.pathname}${page.url.search}`,
		`Route: ${page.route.id ?? ''}${params ? `  ${params}` : ''}`,
		`Signed in as ${[account.displayName, account.role].filter(Boolean).join(', ')}`,
		host ? `Host: ${host}${live.inMesh ? `, mesh of ${plural(live.nodes.size, 'node')}` : ''}` : '',
		line('Instances', [...live.instances.values()].filter((i) => i.state !== InstanceState.STOPPED).map((i) => `${i.name} ${enumLabel(InstanceStateSchema, i.state)}`)),
		line('Slots', [...live.slots.values()].map((s) => `${s.name} ${enumLabel(SlotStateSchema, s.state)}`)),
		line('Routes ready', [...live.routes.values()].filter((r) => r.status?.state === RouteState.READY).map((r) => r.name)),
		line('Tasks running', live.activeTasks.map((t) => t.title))
	];
	return `${lead}\n\n${lines.filter(Boolean).join('\n')}`;
}

// Sets what every request carries: the tools on offer, each call coming back to the panel, and the system text
function arm(): void {
	const s = session.settings;
	s.approveTools = true;
	s.mutatingTools = true;
	s.toolServers = servers.map((x) => x.ref || x.server?.name || '');
	s.system = [instructions, context()].filter(Boolean).join('\n\n');
}

// Lists the gateway's routes, keeping the remembered text route while it is ready and taking the first ready one otherwise
async function loadModels(): Promise<void> {
	models = await listModels();
	const ready = models.filter((m) => m.ready && modelMode(m) === 'text');
	session.model = (ready.find((m) => m.id === prefs.morpheusModel) ?? ready[0])?.id ?? '';
}

async function load(): Promise<void> {
	try {
		const [server] = await Promise.all([connect(), loadModels()]);
		tools = server.tools;
		instructions = server.instructions;
		failure = '';
	} catch (err) {
		failure = reason(err);
	} finally {
		loaded = true;
	}
}

// Tells the model a followed task ended, in a turn the panel draws as a line of its own
async function tell(task: Task): Promise<void> {
	following = following.filter((id) => id !== task.id);
	const turn = create(ChatTurnSchema, { id: crypto.randomUUID(), role: 'user', text: `${task.title} ${enumLabel(TaskStateSchema, task.state)}${task.error ? `: ${task.error}` : ''}`, startedAt: timestampNow() });
	notes[turn.id] = task.state;
	session.conversation.turns = [...session.turns, turn];
	arm();
	await session.answer(servers);
}

// Reads the thread the tab kept, the calls of its last answer nothing answered waiting for the person again
function restore(): void {
	try {
		const raw = sessionStorage.getItem(kept);
		if (!raw) return;
		const was = JSON.parse(raw) as { conversation: JsonValue; following?: string[]; notes?: Record<string, TaskState>; owner?: string };
		session.conversation = fromJson(ConversationSchema, was.conversation);
		following = was.following ?? [];
		notes = was.notes ?? {};
		owner = was.owner ?? '';
		const last = session.turns.findLastIndex((t) => t.role === 'assistant');
		if (last < 0 || session.turns.slice(last).some((t) => t.role === 'user')) return;
		const answered = new Set(session.turns.filter((t) => t.role === 'tool').map((t) => t.toolCallId));
		session.pending = (session.turns[last]?.toolCalls ?? []).filter((c) => !answered.has(c.id)).map((c) => ({ id: c.id, name: c.name, arguments: c.arguments }));
	} catch {
		return;
	}
}

// Clears the thread and what it followed
function clear(): void {
	session.newConversation();
	following = [];
	notes = {};
}

restore();

$effect.root(() => {
	$effect(() => {
		const thread = JSON.stringify({ conversation: toJson(ConversationSchema, session.conversation), following, notes, owner });
		try {
			sessionStorage.setItem(kept, thread);
		} catch {
			// A thread too long to keep leaves nothing behind, so a reload starts empty instead of on an older copy
			sessionStorage.removeItem(kept);
		}
	});
	// A thread kept under one account is dropped once the tab is signed in as another, or as none
	$effect(() => {
		const subject = account.user?.subject ?? '';
		if (!account.ready || subject === owner) return;
		untrack(clear);
		owner = subject;
	});
	// The tools and the routes are listed again when the event stream comes back, as after the daemon restarts
	$effect(() => {
		if (live.ready && untrack(() => loaded)) untrack(() => (loading = load()));
	});
	// One followed task that has ended is told once the session is idle
	$effect(() => {
		if (!loaded || session.busy || session.pending.length > 0 || session.model === '') return;
		const task = following.map((id) => live.tasks.get(id)).find((t) => ended(t?.state));
		if (task) untrack(() => void tell(task));
	});
});

// The assistant in the shell: whether its panel is open, its one thread, and what it is offered
export const morpheus = {
	session,
	// Whether the open page is one Morpheus is offered on
	get here(): boolean {
		return pageOf(page.url.pathname)?.morpheus === true;
	},
	get open(): boolean {
		return prefs.morpheusOpen;
	},
	set open(on: boolean) {
		prefs.morpheusOpen = on;
	},
	get text(): string {
		return text;
	},
	set text(value: string) {
		text = value;
	},
	get servers(): ToolServerStatus[] {
		return servers;
	},
	get models(): ModelEntry[] {
		return models;
	},
	get loaded(): boolean {
		return loaded;
	},
	// Why the tools or the routes could not be listed, empty when they were
	get failure(): string {
		return failure;
	},
	get looking(): string {
		return looking();
	},
	// The asks for the open page, the failed record it shows asked about first
	get asks(): string[] {
		const id = page.route.id ?? '';
		if (id === '/serve/[[instance]]' && live.instances.get(page.params['instance'] ?? '')?.state === InstanceState.FAILED) return ['Why did this fail?'];
		if (id === '/tasks/[[id]]' && live.tasks.get(page.params['id'] ?? '')?.state === TaskState.FAILED) return ['What went wrong here?'];
		return asks[id] ?? ['What can this host run well?'];
	},
	// The calls waiting for the person, every pending one while nothing runs
	get waiting(): PendingCall[] {
		return session.busy ? session.pending.filter((c) => !auto(c)) : session.pending;
	},
	// The state a followed task ended in when the turn is the panel's word of it
	note(turn: ChatTurn): TaskState | undefined {
		return notes[turn.id];
	},
	// Lists the tools and the routes the first time the panel is shown, and the routes again as they change
	start(): Promise<void> {
		if (!loading) {
			let timer: ReturnType<typeof setTimeout> | undefined;
			live.on(EventKind.ROUTE, () => {
				clearTimeout(timer);
				timer = setTimeout(() => void loadModels().catch(() => undefined), 300);
			});
		}
		return (loading ??= load());
	},
	retry(): Promise<void> {
		loaded = false;
		return (loading = load());
	},
	pick(model: string): void {
		session.model = model;
		prefs.morpheusModel = model;
	},
	async send(prompt: string): Promise<void> {
		arm();
		await session.send(prompt, [], servers);
	},
	// Opens the panel and sends a message, which stays in the box when it cannot be sent yet
	async ask(prompt: string): Promise<void> {
		prefs.morpheusOpen = true;
		await this.start();
		if (!prompt) return;
		if (session.busy || session.model === '' || failure) text = prompt;
		else await this.send(prompt);
	},
	// Writes the last answer again, dropping it and whatever followed it
	async again(): Promise<void> {
		const last = session.turns.findLastIndex((t) => t.role === 'assistant');
		if (last < 0) return;
		session.pending = [];
		session.conversation.turns = session.turns.slice(0, last);
		arm();
		await session.answer(servers);
	},
	run(c: PendingCall): Promise<void> {
		return session.runCall(c, servers);
	},
	skip(c: PendingCall): Promise<void> {
		return session.skipCall(c, servers);
	},
	reset: clear
};
