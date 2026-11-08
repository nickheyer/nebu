import { clone, create } from '@bufbuild/protobuf';
import { rpc } from '#lib/api/transport.js';
import { ms, num } from '#lib/format.js';
import {
	ActionKind,
	ActionSchema,
	AutomationSchema,
	BotSpecSchema,
	BotState,
	CommandsSchema,
	EngagementSchema,
	HumanizeSchema,
	MediaSchema,
	MemorySchema,
	PersonaSchema,
	PresenceSchema,
	SamplingSchema,
	TriggerKind,
	TriggerSchema,
	type Automation,
	type Bot,
	type BotGuild,
	type BotSpec,
	type Persona
} from '#lib/proto/nebu/v1/bot_pb.js';
import { RouteState } from '#lib/proto/nebu/v1/gateway_pb.js';
import type { Option, PickerFetch } from '#lib/ui/fields/types.js';

export const botTabs = [
	{ id: 'status', label: 'Status' },
	{ id: 'personas', label: 'Personas' },
	{ id: 'automations', label: 'Automations' },
	{ id: 'behaviour', label: 'Behaviour' },
	{ id: 'activity', label: 'Activity' },
	{ id: 'send', label: 'Send' }
] as const;
export type BotTab = (typeof botTabs)[number]['id'];

export function isBotTab(s: string): s is BotTab {
	return botTabs.some((t) => t.id === s);
}

export const triggerKinds = [
	{ value: TriggerKind.SCHEDULE, label: 'Schedule' },
	{ value: TriggerKind.MESSAGE, label: 'Message' },
	{ value: TriggerKind.KEYWORD, label: 'Keyword' },
	{ value: TriggerKind.MENTION, label: 'Mention' },
	{ value: TriggerKind.MEMBER_JOIN, label: 'Member join' },
	{ value: TriggerKind.REACTION, label: 'Reaction' },
	{ value: TriggerKind.COMMAND, label: 'Command' }
];

export const actionKinds = [
	{ value: ActionKind.CHAT, label: 'Chat' },
	{ value: ActionKind.IMAGE, label: 'Image' },
	{ value: ActionKind.VIDEO, label: 'Video' },
	{ value: ActionKind.TEXT, label: 'Text' },
	{ value: ActionKind.REACT, label: 'React' },
	{ value: ActionKind.PRESENCE, label: 'Presence' }
];

export const sendKinds = [
	{ value: ActionKind.TEXT, label: 'Text' },
	{ value: ActionKind.CHAT, label: 'Chat' },
	{ value: ActionKind.IMAGE, label: 'Image' },
	{ value: ActionKind.VIDEO, label: 'Video' }
];

export const activityLevels = [
	{ value: 'info', label: 'Info' },
	{ value: 'warn', label: 'Warn' },
	{ value: 'error', label: 'Error' }
];
export const activityKinds = ['reply', 'image', 'video', 'command', 'automation', 'reaction', 'shard', 'presence'].map((k) => ({ value: k, label: k[0]!.toUpperCase() + k.slice(1) }));

export const presenceStatuses = [
	{ value: 'online', label: 'Online' },
	{ value: 'idle', label: 'Idle' },
	{ value: 'dnd', label: 'Do not disturb' },
	{ value: 'invisible', label: 'Invisible' }
];
export const activityTypes = ['playing', 'listening', 'watching', 'competing', 'custom'].map((a) => ({ value: a, label: a[0]!.toUpperCase() + a.slice(1) }));

export const commandRoles = ['ask', 'imagine', 'video', 'persona', 'models', 'reset', 'help'];
export const imageSizes = ['512x512', '768x768', '1024x1024', '1024x768', '768x1024'];
export const videoSizes = ['512x512', '768x432', '432x768', '640x360', '1024x576'];
export const fpsOptions = [8, 12, 16, 24, 30];
export const templateVariables = '.Content .Author .Channel .Guild .Now .Persona .Bot';

// Lowercase word naming a trigger kind in a row
export function triggerWord(kind: TriggerKind): string {
	return (triggerKinds.find((k) => k.value === kind)?.label ?? '').toLowerCase();
}

// Lowercase word naming an action kind in a row
export function actionWord(kind: ActionKind): string {
	return (actionKinds.find((k) => k.value === kind)?.label ?? '').toLowerCase();
}

// Whether the bot holds or seeks a connection
export function running(b: Bot): boolean {
	return b.state !== BotState.STOPPED && b.state !== BotState.UNSPECIFIED;
}

// Username, shards with their mean latency, and the counters of a bot, one per line
export function botTip(b: Bot): string {
	const st = b.status;
	const shards = st?.shards ?? [];
	const latency = shards.length ? shards.reduce((sum, s) => sum + s.latencyMs, 0) / shards.length : undefined;
	return [
		st?.username ? `username ${st.username}` : '',
		`shards ${num(shards.length)}${latency === undefined ? '' : `, latency ${ms(latency)}`}`,
		`replies ${num(st?.replies)}`,
		`images ${num(st?.images)}`,
		`videos ${num(st?.videos)}`,
		`errors ${num(st?.errors)}`
	]
		.filter(Boolean)
		.join('\n');
}

// A working copy of a bot's spec for the settings tabs
export function specCopy(bot: Bot | undefined): BotSpec {
	return bot?.spec ? clone(BotSpecSchema, bot.spec) : create(BotSpecSchema);
}

// Fills the parts the editors write into with the defaults the daemon gives a missing part
export function withParts(spec: BotSpec): BotSpec {
	spec.engagement ??= create(EngagementSchema, { directMessages: true, requireMention: true });
	spec.commands ??= create(CommandsSchema, { enabled: true });
	spec.memory ??= create(MemorySchema, { includeNames: true });
	spec.media ??= create(MediaSchema);
	spec.presence ??= create(PresenceSchema);
	for (const p of spec.personas) {
		p.sampling ??= create(SamplingSchema);
		p.humanize ??= create(HumanizeSchema);
	}
	for (const a of spec.automations) {
		a.trigger ??= create(TriggerSchema, { kind: TriggerKind.MESSAGE });
		a.action ??= create(ActionSchema, { kind: ActionKind.CHAT });
	}
	return spec;
}

function newId(): string {
	return crypto.randomUUID().slice(0, 8);
}

export function newPersona(): Persona {
	return create(PersonaSchema, { id: newId(), name: 'New persona', sampling: create(SamplingSchema), humanize: create(HumanizeSchema) });
}

// A copy of the persona under a new id and name
export function duplicatePersona(p: Persona): Persona {
	const copy = clone(PersonaSchema, p);
	copy.id = newId();
	copy.name = `${p.name} copy`;
	return copy;
}

export function newAutomation(): Automation {
	return create(AutomationSchema, { id: newId(), name: 'New automation', enabled: true, trigger: create(TriggerSchema, { kind: TriggerKind.MESSAGE }), action: create(ActionSchema, { kind: ActionKind.CHAT }) });
}

// Guilds a running bot sees as checklist options with their member counts
export function guildOptions(guilds: BotGuild[]): Option<string>[] {
	return guilds.map((g) => ({ value: g.id, label: g.name, description: `${num(g.members)} members` }));
}

// Ready routes answering with the modality, queried by name as typed, every ready route without one
export function routeFetch(modality = ''): PickerFetch<string> {
	return async (query, cursor, limit, signal) => {
		const res = await rpc.gateway.listRoutes({ query, cursor, limit, state: RouteState.READY, modality }, { signal });
		return { items: res.routes.map((r) => ({ value: r.name, label: r.name, description: r.status?.inventory?.modalities.join(', ') || undefined })), next: res.nextCursor };
	};
}

export const timezones = Intl.supportedValuesOf('timeZone').map((z) => ({ value: z, label: z }));
