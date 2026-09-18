import { create } from '@bufbuild/protobuf';
import { ActionKind, BotState, BotSpecSchema, TriggerKind, type Automation, type BotSpec, type Humanize, type Persona, type Sampling } from '$proto/bot_pb';
import { ApiFlavor } from '$proto/runtime_pb';
import { RouteState, type Route } from '$proto/gateway_pb';
import { stateLabel } from './format';

// Default slash command names.
export const commandRoles = ['ask', 'imagine', 'video', 'persona', 'models', 'reset', 'help'] as const;
export type CommandRole = (typeof commandRoles)[number];

export const commandBlurbs: Record<CommandRole, string> = {
  ask: 'Ask a question',
  imagine: 'Generate an image',
  video: 'Generate a video',
  persona: 'Switch channel persona',
  models: 'List served models',
  reset: 'Clear channel history',
  help: 'List commands'
};

export const presenceStatuses = [
  { value: 'online', label: 'Online' },
  { value: 'idle', label: 'Idle' },
  { value: 'dnd', label: 'Do not disturb' },
  { value: 'invisible', label: 'Invisible' }
];

export const activityTypes = [
  { value: 'playing', label: 'Playing' },
  { value: 'listening', label: 'Listening to' },
  { value: 'watching', label: 'Watching' },
  { value: 'competing', label: 'Competing in' },
  { value: 'custom', label: 'Custom' }
];

export const triggerKinds: { value: string; label: string; detail: string }[] = [
  { value: String(TriggerKind.SCHEDULE), label: 'Schedule', detail: 'Cron or interval' },
  { value: String(TriggerKind.MESSAGE), label: 'Message', detail: 'Any visible message' },
  { value: String(TriggerKind.KEYWORD), label: 'Keyword', detail: 'Regex match' },
  { value: String(TriggerKind.MENTION), label: 'Mention', detail: 'Bot mentioned' },
  { value: String(TriggerKind.MEMBER_JOIN), label: 'Member join', detail: 'New guild member' },
  { value: String(TriggerKind.REACTION), label: 'Reaction', detail: 'Reaction added' },
  { value: String(TriggerKind.COMMAND), label: 'Command', detail: 'Prefix command, e.g. !roll' }
];

export const actionKinds: { value: string; label: string; detail: string }[] = [
  { value: String(ActionKind.CHAT), label: 'Chat', detail: 'Generate a reply' },
  { value: String(ActionKind.IMAGE), label: 'Image', detail: 'Generate an image' },
  { value: String(ActionKind.VIDEO), label: 'Video', detail: 'Generate a video' },
  { value: String(ActionKind.TEXT), label: 'Text', detail: 'Post the rendered template' },
  { value: String(ActionKind.REACT), label: 'React', detail: 'Add an emoji reaction' },
  { value: String(ActionKind.PRESENCE), label: 'Presence', detail: 'Set activity text' }
];

export const postKinds: { value: string; label: string; detail: string }[] = [
  { value: String(ActionKind.TEXT), label: 'Text', detail: 'Posted as written' },
  { value: String(ActionKind.CHAT), label: 'Chat', detail: 'Generate a reply' },
  { value: String(ActionKind.IMAGE), label: 'Image', detail: 'Generate an image' },
  { value: String(ActionKind.VIDEO), label: 'Video', detail: 'Generate a video' }
];

export const templateFields = ['{{.Content}}', '{{.Author}}', '{{.Channel}}', '{{.Guild}}', '{{.Now}}', '{{.Persona}}', '{{.Bot}}', '{{.Emoji}}', '{{index .Match 1}}'];

// Numeric fields stay as strings so blanks can use defaults.
export interface SamplingFields {
  temperature: string;
  topP: string;
  topK: string;
  maxTokens: string;
  seed: string;
  stop: string[];
}

export interface HumanizeFields {
  enabled: boolean;
  charsPerSecond: string;
  delayMinMs: string;
  delayMaxMs: string;
  maxTypingMs: string;
  splitMessages: boolean;
  maxChunkChars: string;
  casual: boolean;
  quoteReply: boolean;
  reactionChance: string;
  reactions: string[];
  ambientReplyChance: string;
  ignoreChance: string;
  activeHours: string;
  timezone: string;
}

export interface PersonaFields {
  id: string;
  name: string;
  avatarUrl: string;
  webhook: boolean;
  stream: boolean;
  vision: boolean;
  systemPrompt: string;
  model: string;
  imageModel: string;
  videoModel: string;
  imageStyle: string;
  negativePrompt: string;
  wakeWords: string[];
  channelIds: string[];
  sampling: SamplingFields;
  humanize: HumanizeFields;
}

export interface AutomationFields {
  id: string;
  name: string;
  enabled: boolean;
  personaId: string;
  triggerKind: string;
  cron: string;
  everySeconds: string;
  pattern: string;
  command: string;
  actionKind: string;
  template: string;
  emoji: string;
  reply: boolean;
  model: string;
  channelIds: string[];
  guildIds: string[];
  chance: string;
  cooldownMs: string;
}

export interface SpecFields {
  shardCount: string;
  shardIds: string[];
  presence: { status: string; activityType: string; activities: string[]; rotateSeconds: string };
  personas: PersonaFields[];
  automations: AutomationFields[];
  engagement: {
    directMessages: boolean;
    requireMention: boolean;
    answerBots: boolean;
    memberEvents: boolean;
    prefix: string;
    channelIds: string[];
    deniedChannelIds: string[];
    guildIds: string[];
    userIds: string[];
    deniedUserIds: string[];
    cooldownMs: string;
    userCooldownMs: string;
    maxConcurrent: string;
    maxBotChain: string;
  };
  commands: { enabled: boolean; guildIds: string[]; names: Record<CommandRole, string>; disabled: string[] };
  memory: { messages: string; windowMinutes: string; includeNames: boolean; maxChars: string };
  media: { videoFrames: string; maxUploadMb: string; imageSize: string; imageSteps: string; imageCount: string; videoSize: string; videoSeconds: string; videoFps: string };
}

// Show zero as blank so placeholders display defaults.
const text = (n: number | bigint | undefined): string => (n ? String(n) : '');
// Show unset numbers as blank.
const opt = (n: number | bigint | undefined): string => (n === undefined ? '' : String(n));
// Parse integers, defaulting to zero.
const whole = (s: string): number => Math.max(0, Math.floor(parseFloat(s) || 0));
// Parse numbers, defaulting to zero.
const num = (s: string): number => {
  const v = parseFloat(s);
  return Number.isFinite(v) ? v : 0;
};
// Leave blank or invalid numbers unset.
const optNum = (s: string): number | undefined => {
  if (s.trim() === '') return undefined;
  const v = parseFloat(s);
  return Number.isFinite(v) ? v : undefined;
};
const optWhole = (s: string): number | undefined => {
  const v = optNum(s);
  return v === undefined ? undefined : Math.floor(v);
};

// Assign IDs before saving so automations can reference new personas.
export function newId(): string {
  const bytes = new Uint8Array(8);
  crypto.getRandomValues(bytes);
  return [...bytes].map((b) => b.toString(16).padStart(2, '0')).join('');
}

export function samplingFields(s: Sampling | undefined): SamplingFields {
  return { temperature: opt(s?.temperature), topP: opt(s?.topP), topK: opt(s?.topK), maxTokens: opt(s?.maxTokens), seed: opt(s?.seed), stop: [...(s?.stop ?? [])] };
}

export function humanizeFields(h: Humanize | undefined): HumanizeFields {
  return {
    enabled: h?.enabled ?? false,
    charsPerSecond: text(h?.charsPerSecond),
    delayMinMs: text(h?.delayMinMs),
    delayMaxMs: text(h?.delayMaxMs),
    maxTypingMs: text(h?.maxTypingMs),
    splitMessages: h?.splitMessages ?? false,
    maxChunkChars: text(h?.maxChunkChars),
    casual: h?.casual ?? false,
    quoteReply: h?.quoteReply ?? false,
    reactionChance: text(h?.reactionChance),
    reactions: [...(h?.reactions ?? [])],
    ambientReplyChance: text(h?.ambientReplyChance),
    ignoreChance: text(h?.ignoreChance),
    activeHours: h?.activeHours ?? '',
    timezone: h?.timezone ?? ''
  };
}

export function personaFields(p?: Persona): PersonaFields {
  return {
    id: p?.id || newId(),
    name: p?.name ?? '',
    avatarUrl: p?.avatarUrl ?? '',
    webhook: p?.webhook ?? false,
    stream: p?.stream ?? false,
    vision: p?.vision ?? true,
    systemPrompt: p?.systemPrompt ?? '',
    model: p?.model ?? '',
    imageModel: p?.imageModel ?? '',
    videoModel: p?.videoModel ?? '',
    imageStyle: p?.imageStyle ?? '',
    negativePrompt: p?.negativePrompt ?? '',
    wakeWords: [...(p?.wakeWords ?? [])],
    channelIds: [...(p?.channelIds ?? [])],
    sampling: samplingFields(p?.sampling),
    humanize: humanizeFields(p?.humanize)
  };
}

export function automationFields(a?: Automation): AutomationFields {
  return {
    id: a?.id || newId(),
    name: a?.name ?? '',
    enabled: a?.enabled ?? true,
    personaId: a?.personaId ?? '',
    triggerKind: a?.trigger?.kind ? String(a.trigger.kind) : String(TriggerKind.MESSAGE),
    cron: a?.trigger?.cron ?? '',
    everySeconds: text(a?.trigger?.everySeconds),
    pattern: a?.trigger?.pattern ?? '',
    command: a?.trigger?.command ?? '',
    actionKind: a?.action?.kind ? String(a.action.kind) : String(ActionKind.CHAT),
    template: a?.action?.template ?? '',
    emoji: a?.action?.emoji ?? '',
    reply: a?.action?.reply ?? false,
    model: a?.action?.model ?? '',
    channelIds: [...(a?.channelIds ?? [])],
    guildIds: [...(a?.guildIds ?? [])],
    chance: text(a?.chance),
    cooldownMs: text(a?.cooldownMs)
  };
}

// Use daemon defaults when no spec exists.
export function specFields(s?: BotSpec): SpecFields {
  const e = s?.engagement;
  const c = s?.commands;
  const names = {} as Record<CommandRole, string>;
  for (const role of commandRoles) names[role] = c?.names[role] && c.names[role] !== role ? c.names[role] : '';
  return {
    shardCount: text(s?.shardCount),
    shardIds: (s?.shardIds ?? []).map(String),
    presence: {
      status: s?.presence?.status ?? 'online',
      activityType: s?.presence?.activityType ?? 'playing',
      activities: [...(s?.presence?.activities ?? [])],
      rotateSeconds: text(s?.presence?.rotateSeconds)
    },
    personas: (s?.personas ?? []).map((p) => personaFields(p)),
    automations: (s?.automations ?? []).map((a) => automationFields(a)),
    engagement: {
      directMessages: e ? e.directMessages : true,
      requireMention: e ? e.requireMention : true,
      answerBots: e?.answerBots ?? false,
      memberEvents: e?.memberEvents ?? false,
      prefix: e?.prefix ?? '',
      channelIds: [...(e?.channelIds ?? [])],
      deniedChannelIds: [...(e?.deniedChannelIds ?? [])],
      guildIds: [...(e?.guildIds ?? [])],
      userIds: [...(e?.userIds ?? [])],
      deniedUserIds: [...(e?.deniedUserIds ?? [])],
      cooldownMs: text(e?.cooldownMs),
      userCooldownMs: text(e?.userCooldownMs),
      maxConcurrent: text(e?.maxConcurrent),
      maxBotChain: text(e?.maxBotChain)
    },
    commands: { enabled: c ? c.enabled : true, guildIds: [...(c?.guildIds ?? [])], names, disabled: [...(c?.disabled ?? [])] },
    memory: { messages: text(s?.memory?.messages), windowMinutes: text(s?.memory?.windowMinutes), includeNames: s?.memory ? s.memory.includeNames : true, maxChars: text(s?.memory?.maxChars) },
    media: {
      videoFrames: text(s?.media?.videoFrames),
      maxUploadMb: s?.media?.maxUploadBytes ? String(Number(s.media.maxUploadBytes) / 1e6) : '',
      imageSize: s?.media?.imageSize ?? '',
      imageSteps: text(s?.media?.imageSteps),
      imageCount: text(s?.media?.imageCount),
      videoSize: s?.media?.videoSize ?? '',
      videoSeconds: text(s?.media?.videoSeconds),
      videoFps: text(s?.media?.videoFps)
    }
  };
}

const trimmed = (items: string[]): string[] => items.map((s) => s.trim()).filter(Boolean);

export function specFrom(f: SpecFields): BotSpec {
  const names: Record<string, string> = {};
  for (const role of commandRoles) if (f.commands.names[role].trim()) names[role] = f.commands.names[role].trim();
  const seed = (s: string): bigint | undefined => {
    const v = optWhole(s);
    return v === undefined ? undefined : BigInt(v);
  };
  return create(BotSpecSchema, {
    shardCount: whole(f.shardCount),
    shardIds: f.shardIds.map(whole),
    presence: { status: f.presence.status, activityType: f.presence.activityType, activities: trimmed(f.presence.activities), rotateSeconds: whole(f.presence.rotateSeconds) },
    personas: f.personas.map((p) => ({
      id: p.id,
      name: p.name.trim(),
      avatarUrl: p.avatarUrl.trim(),
      webhook: p.webhook,
      stream: p.stream,
      vision: p.vision,
      systemPrompt: p.systemPrompt,
      model: p.model.trim(),
      imageModel: p.imageModel.trim(),
      videoModel: p.videoModel.trim(),
      imageStyle: p.imageStyle.trim(),
      negativePrompt: p.negativePrompt.trim(),
      wakeWords: trimmed(p.wakeWords),
      channelIds: trimmed(p.channelIds),
      sampling: { temperature: optNum(p.sampling.temperature), topP: optNum(p.sampling.topP), topK: optWhole(p.sampling.topK), maxTokens: optWhole(p.sampling.maxTokens), stop: trimmed(p.sampling.stop), seed: seed(p.sampling.seed) },
      humanize: {
        enabled: p.humanize.enabled,
        charsPerSecond: num(p.humanize.charsPerSecond),
        delayMinMs: whole(p.humanize.delayMinMs),
        delayMaxMs: whole(p.humanize.delayMaxMs),
        maxTypingMs: whole(p.humanize.maxTypingMs),
        splitMessages: p.humanize.splitMessages,
        maxChunkChars: whole(p.humanize.maxChunkChars),
        casual: p.humanize.casual,
        quoteReply: p.humanize.quoteReply,
        reactionChance: num(p.humanize.reactionChance),
        reactions: trimmed(p.humanize.reactions),
        ambientReplyChance: num(p.humanize.ambientReplyChance),
        ignoreChance: num(p.humanize.ignoreChance),
        activeHours: p.humanize.activeHours.trim(),
        timezone: p.humanize.timezone.trim()
      }
    })),
    automations: f.automations.map((a) => ({
      id: a.id,
      name: a.name.trim(),
      enabled: a.enabled,
      personaId: a.personaId,
      trigger: { kind: Number(a.triggerKind) as TriggerKind, cron: a.cron.trim(), everySeconds: whole(a.everySeconds), pattern: a.pattern, command: a.command.trim() },
      action: { kind: Number(a.actionKind) as ActionKind, template: a.template, emoji: a.emoji.trim(), reply: a.reply, model: a.model.trim() },
      channelIds: trimmed(a.channelIds),
      guildIds: trimmed(a.guildIds),
      chance: num(a.chance),
      cooldownMs: whole(a.cooldownMs)
    })),
    engagement: {
      directMessages: f.engagement.directMessages,
      requireMention: f.engagement.requireMention,
      answerBots: f.engagement.answerBots,
      memberEvents: f.engagement.memberEvents,
      prefix: f.engagement.prefix.trim(),
      channelIds: trimmed(f.engagement.channelIds),
      deniedChannelIds: trimmed(f.engagement.deniedChannelIds),
      guildIds: trimmed(f.engagement.guildIds),
      userIds: trimmed(f.engagement.userIds),
      deniedUserIds: trimmed(f.engagement.deniedUserIds),
      cooldownMs: whole(f.engagement.cooldownMs),
      userCooldownMs: whole(f.engagement.userCooldownMs),
      maxConcurrent: whole(f.engagement.maxConcurrent),
      maxBotChain: whole(f.engagement.maxBotChain)
    },
    commands: { enabled: f.commands.enabled, guildIds: trimmed(f.commands.guildIds), names, disabled: [...f.commands.disabled] },
    memory: { messages: whole(f.memory.messages), windowMinutes: whole(f.memory.windowMinutes), includeNames: f.memory.includeNames, maxChars: whole(f.memory.maxChars) },
    media: {
      videoFrames: whole(f.media.videoFrames),
      maxUploadBytes: BigInt(Math.round(num(f.media.maxUploadMb) * 1e6)),
      imageSize: f.media.imageSize.trim(),
      imageSteps: whole(f.media.imageSteps),
      imageCount: whole(f.media.imageCount),
      videoSize: f.media.videoSize.trim(),
      videoSeconds: num(f.media.videoSeconds),
      videoFps: whole(f.media.videoFps)
    }
  });
}

export type ModelKind = 'chat' | 'image' | 'video';

export function modelKindOf(action: ActionKind): ModelKind | undefined {
  switch (action) {
    case ActionKind.CHAT:
      return 'chat';
    case ActionKind.IMAGE:
      return 'image';
    case ActionKind.VIDEO:
      return 'video';
    default:
      return undefined;
  }
}

// Matching gateway routes, sorted by name.
export function routesFor(kind: ModelKind, routes: Iterable<Route>): Route[] {
  const out = [...routes].filter((r) => {
    if (kind === 'image') return r.modes.includes('img_gen');
    if (kind === 'video') return r.modes.includes('vid_gen');
    return r.api !== ApiFlavor.SDCPP;
  });
  return out.sort((a, b) => a.name.localeCompare(b.name));
}

export function routeDetail(r: Route): string {
  return r.state === RouteState.READY ? (r.model ? `ready (${r.model})` : 'ready') : stateLabel(RouteState, r.state).toLowerCase();
}

// Guild lookup and posting require a connected bot.
export function botRunning(state: BotState | undefined): boolean {
  return state === BotState.READY || state === BotState.DEGRADED;
}

export function botStartable(state: BotState | undefined): boolean {
  return state === BotState.STOPPED || state === BotState.FAILED || state === BotState.UNSPECIFIED;
}

export function botStoppable(state: BotState | undefined): boolean {
  return state === BotState.READY || state === BotState.DEGRADED || state === BotState.CONNECTING;
}

export function activityTone(level: string): 'neutral' | 'warn' | 'bad' {
  if (level === 'error') return 'bad';
  if (level === 'warn') return 'warn';
  return 'neutral';
}
