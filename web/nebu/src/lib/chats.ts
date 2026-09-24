// Saved conversations: the daemon keeps one store per signed-in account. Image
// bytes live there too, cached in this browser's IndexedDB once fetched.
import { timestampFromMs, timestampMs } from '@bufbuild/protobuf/wkt';
import type { Timestamp } from '@bufbuild/protobuf/wkt';
import type { MessageInitShape } from '@bufbuild/protobuf';
import { api } from './api';
import type { Conversation, ChatFile, ChatTurn, ChatFileSchema, ChatTurnSchema, ConversationSchema } from '$proto/chat_pb';
import type { Dialect, ToolCall } from './chatClient';
import { getImage, putImage, type Attachment } from './images';

// An image the model answered with
export interface Media {
  key: string;
  id?: string;
  url?: string;
  error?: string;
}

export interface Turn {
  role: 'user' | 'assistant';
  text: string;
  images?: Attachment[];
  media?: Media[];
  toolCalls?: ToolCall[];
  error?: string;
  trace?: string;
  startedAt?: number;
  firstTokenAt?: number;
  finishedAt?: number;
  promptTokens?: number;
  completionTokens?: number;
  stop?: string;
  dialect?: Dialect;
}

export interface Session {
  turns: Turn[];
  system: string;
  dialect: Dialect;
  stream: boolean;
  temperature: string;
  topP: string;
  topK: string;
  maxTokens: string;
  stop: string;
  seed: string;
  tools: string;
}

export const blank = (): Session => ({ turns: [], system: '', dialect: 'openai', stream: true, temperature: '', topP: '', topK: '', maxTokens: '', stop: '', seed: '', tools: '' });

// A session with the same settings and no turns.
export const fresh = (from: Session): Session => ({ ...from, turns: [] });

const dialects: Dialect[] = ['openai', 'anthropic', 'ollama'];

function ms(ts: Timestamp | undefined): number | undefined {
  return ts ? timestampMs(ts) : undefined;
}

function stamp(n: number | undefined): Timestamp | undefined {
  return n ? timestampFromMs(n) : undefined;
}

function fileOf(a: Attachment): MessageInitShape<typeof ChatFileSchema> {
  return { id: a.id, mediaType: a.mediaType, width: a.width, height: a.height, sizeBytes: BigInt(a.bytes), name: a.name };
}

function attachmentOf(f: ChatFile): Attachment {
  return { id: f.id, mediaType: f.mediaType, width: f.width, height: f.height, bytes: Number(f.sizeBytes), name: f.name };
}

function mediaOf(f: ChatFile, i: number): Media {
  const m: Media = { key: f.id || f.url || `media-${i}` };
  if (f.id) m.id = f.id;
  if (f.url) m.url = f.url;
  if (f.error) m.error = f.error;
  return m;
}

function turnToProto(t: Turn): MessageInitShape<typeof ChatTurnSchema> {
  return {
    role: t.role,
    text: t.text,
    images: (t.images ?? []).map(fileOf),
    media: (t.media ?? []).map((m) => ({ id: m.id ?? '', url: m.url ?? '', error: m.error ?? '' })),
    toolCalls: (t.toolCalls ?? []).map((c) => ({ id: c.id, name: c.name, arguments: c.arguments })),
    error: t.error ?? '',
    traceId: t.trace ?? '',
    startedAt: stamp(t.startedAt),
    firstTokenAt: stamp(t.firstTokenAt),
    finishedAt: stamp(t.finishedAt),
    promptTokens: t.promptTokens ?? 0,
    completionTokens: t.completionTokens ?? 0,
    stop: t.stop ?? '',
    dialect: t.dialect ?? ''
  };
}

function turnFromProto(t: ChatTurn): Turn {
  const out: Turn = { role: t.role === 'assistant' ? 'assistant' : 'user', text: t.text };
  if (t.images.length) out.images = t.images.map(attachmentOf);
  if (t.media.length) out.media = t.media.map(mediaOf);
  if (t.toolCalls.length) out.toolCalls = t.toolCalls.map((c) => ({ id: c.id, name: c.name, arguments: c.arguments }));
  if (t.error) out.error = t.error;
  if (t.traceId) out.trace = t.traceId;
  const startedAt = ms(t.startedAt);
  if (startedAt) out.startedAt = startedAt;
  const firstTokenAt = ms(t.firstTokenAt);
  if (firstTokenAt) out.firstTokenAt = firstTokenAt;
  const finishedAt = ms(t.finishedAt);
  if (finishedAt) out.finishedAt = finishedAt;
  if (t.promptTokens) out.promptTokens = t.promptTokens;
  if (t.completionTokens) out.completionTokens = t.completionTokens;
  if (t.stop) out.stop = t.stop;
  if (dialects.includes(t.dialect as Dialect)) out.dialect = t.dialect as Dialect;
  return out;
}

// The message the daemon stores for a conversation.
export function toConversation(id: string, title: string, model: string, s: Session): MessageInitShape<typeof ConversationSchema> {
  return {
    id,
    title,
    model,
    settings: { system: s.system, dialect: s.dialect, stream: s.stream, temperature: s.temperature, topP: s.topP, topK: s.topK, maxTokens: s.maxTokens, stop: s.stop, seed: s.seed, tools: s.tools },
    turns: s.turns.map(turnToProto)
  };
}

export function fromConversation(c: Conversation): Session {
  const st = c.settings;
  const dialect = dialects.includes(st?.dialect as Dialect) ? (st?.dialect as Dialect) : 'openai';
  return {
    turns: c.turns.map(turnFromProto),
    system: st?.system ?? '',
    dialect,
    stream: st ? st.stream : true,
    temperature: st?.temperature ?? '',
    topP: st?.topP ?? '',
    topK: st?.topK ?? '',
    maxTokens: st?.maxTokens ?? '',
    stop: st?.stop ?? '',
    seed: st?.seed ?? '',
    tools: st?.tools ?? ''
  };
}

// Ids of every image kept under an id in the turns.
export function fileIds(turns: Turn[]): string[] {
  return turns.flatMap((t) => [...(t.images ?? []).map((a) => a.id), ...(t.media ?? []).flatMap((m) => (m.id ? [m.id] : []))]);
}

// Loads an image from this browser's cache, then from the daemon's store.
export async function loadFile(id: string): Promise<Blob | undefined> {
  const cached = await getImage(id);
  if (cached) return cached;
  const r = await api.chats.getChatFile({ id });
  if (!r.file) return undefined;
  const blob = new Blob([r.data as BlobPart], { type: r.file.mediaType });
  await putImage(id, blob);
  return blob;
}

// Stores an image with the daemon so the conversation shows it anywhere the account signs in.
export async function uploadFile(a: Attachment, blob: Blob): Promise<void> {
  const data = new Uint8Array(await blob.arrayBuffer());
  await api.chats.putChatFile({ file: fileOf(a), data });
}

// Removes images the daemon no longer needs.
export async function deleteFiles(ids: string[]): Promise<void> {
  if (ids.length) await api.chats.deleteChatFiles({ ids });
}

// A preview title while the daemon has not named the conversation yet.
export function previewTitle(turns: Turn[]): string {
  const first = turns.find((t) => t.role === 'user');
  if (!first) return 'New chat';
  const line = first.text.trim().split('\n')[0]?.trim() ?? '';
  if (!line) return first.images && first.images.length > 1 ? `${first.images.length} images` : 'Image';
  return line.length > 60 ? line.slice(0, 60).trim() + '…' : line;
}
