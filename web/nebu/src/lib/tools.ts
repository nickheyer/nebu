import { ToolServerKind, type ToolServer } from '$proto/settings_pb';
import { ToolServerState, type ToolServerStatus } from '$proto/tools_pb';
import { live } from './state.svelte';

export const kindItems: { value: string; label: string; detail: string }[] = [
  { value: String(ToolServerKind.SEARCH), label: 'Web search', detail: 'SearXNG or Brave, offered as web_search' },
  { value: String(ToolServerKind.FETCH), label: 'Web fetch', detail: 'Reads pages as text, offered as web_fetch' },
  { value: String(ToolServerKind.STDIO), label: 'Command', detail: 'An MCP server run as a process' },
  { value: String(ToolServerKind.HTTP), label: 'URL', detail: 'An MCP server over streamable HTTP' }
];

export const kindLabel = (k: ToolServerKind) => kindItems.find((i) => i.value === String(k))?.label ?? 'Unknown';

export const providerItems = [
  { value: 'searxng', label: 'SearXNG' },
  { value: 'brave', label: 'Brave Search' }
];

// Text fields so blanks stay blank in the form
export interface ToolServerFields {
  id: string;
  name: string;
  enabled: boolean;
  kind: string;
  provider: string;
  searxngUrl: string;
  braveKey: string;
  command: string;
  args: string[];
  env: string;
  url: string;
  headers: string;
  allowedTools: string[];
}

export function toolServerFields(s?: ToolServer): ToolServerFields {
  return {
    id: s?.id ?? '',
    name: s?.name ?? '',
    enabled: s ? s.enabled : true,
    kind: String(s?.kind ?? ToolServerKind.SEARCH),
    provider: s?.provider || 'searxng',
    searxngUrl: s?.searxngUrl ?? '',
    braveKey: s?.braveKey ?? '',
    command: s?.command ?? '',
    args: [...(s?.args ?? [])],
    env: pairsText(s?.env),
    url: s?.url ?? '',
    headers: pairsText(s?.headers),
    allowedTools: [...(s?.allowedTools ?? [])]
  };
}

export function toolServerFrom(f: ToolServerFields): ToolServer {
  const kind = Number(f.kind) as ToolServerKind;
  return {
    id: f.id,
    name: f.name.trim(),
    enabled: f.enabled,
    kind,
    provider: kind === ToolServerKind.SEARCH ? f.provider : '',
    searxngUrl: kind === ToolServerKind.SEARCH && f.provider === 'searxng' ? f.searxngUrl.trim() : '',
    braveKey: kind === ToolServerKind.SEARCH && f.provider === 'brave' ? f.braveKey.trim() : '',
    command: kind === ToolServerKind.STDIO ? f.command.trim() : '',
    args: kind === ToolServerKind.STDIO ? f.args.map((a) => a.trim()).filter(Boolean) : [],
    env: kind === ToolServerKind.STDIO ? pairsFrom(f.env) : {},
    url: kind === ToolServerKind.HTTP ? f.url.trim() : '',
    headers: kind === ToolServerKind.HTTP ? pairsFrom(f.headers) : {},
    allowedTools: f.allowedTools.map((a) => a.trim()).filter(Boolean)
  } as ToolServer;
}

// One KEY=value per line
function pairsText(m: Record<string, string> | undefined): string {
  return Object.entries(m ?? {})
    .map(([k, v]) => `${k}=${v}`)
    .join('\n');
}

function pairsFrom(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split('\n')) {
    const i = line.indexOf('=');
    if (i <= 0) continue;
    const key = line.slice(0, i).trim();
    if (key) out[key] = line.slice(i + 1).trim();
  }
  return out;
}

// What keeps a form from saving, empty when nothing does
export function toolServerProblem(f: ToolServerFields, taken: string[]): string {
  const name = f.name.trim();
  if (!name) return 'Name required';
  if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$/.test(name) || name.includes('__')) return 'Letters, digits, - and _, no double underscore';
  if (taken.includes(name)) return 'That name is taken';
  const kind = Number(f.kind) as ToolServerKind;
  if (kind === ToolServerKind.SEARCH && f.provider === 'searxng' && !/^https?:\/\/.+/.test(f.searxngUrl.trim())) return 'SearXNG URL required';
  if (kind === ToolServerKind.SEARCH && f.provider === 'brave' && !f.braveKey.trim()) return 'Brave token required';
  if (kind === ToolServerKind.STDIO && !f.command.trim()) return 'Command required';
  if (kind === ToolServerKind.HTTP && !/^https?:\/\/.+/.test(f.url.trim())) return 'URL required';
  return '';
}

export function stateTone(s: ToolServerState): 'ok' | 'warn' | 'bad' | 'neutral' {
  switch (s) {
    case ToolServerState.READY:
      return 'ok';
    case ToolServerState.CONNECTING:
      return 'warn';
    case ToolServerState.FAILED:
      return 'bad';
    default:
      return 'neutral';
  }
}

export function stateLabel(s: ToolServerState): string {
  switch (s) {
    case ToolServerState.READY:
      return 'Ready';
    case ToolServerState.CONNECTING:
      return 'Connecting';
    case ToolServerState.FAILED:
      return 'Failed';
    case ToolServerState.DISABLED:
      return 'Off';
    default:
      return 'Unknown';
  }
}

// This node's servers in Settings order
export function toolServers(): ToolServerStatus[] {
  const order = live.settings?.toolServers.map((s) => s.name) ?? [];
  return [...live.toolServers.values()].sort((a, b) => order.indexOf(a.server?.name ?? '') - order.indexOf(b.server?.name ?? ''));
}

// Every tool the gateway can run right now, by the name a model calls it, with whether it changes anything
export function gatewayTools(): Map<string, { server: string; readOnly: boolean }> {
  const out = new Map<string, { server: string; readOnly: boolean }>();
  for (const st of live.toolServers.values()) {
    if (st.state !== ToolServerState.READY) continue;
    for (const t of st.tools) out.set(t.name, { server: st.server?.name ?? '', readOnly: t.readOnly });
  }
  return out;
}

// The attachment text a profile or persona shows
export function attachmentText(refs: string[], mutating: boolean): string {
  if (!refs.length) return '';
  return `Tools: ${refs.join(', ')}${mutating ? ' (mutating too)' : ''}`;
}
