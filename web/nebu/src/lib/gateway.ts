import { SystemMessages, SystemPrompt, type Policy, type Profile } from '$proto/gateway_pb';
import { ToolSupport, type TemplateProbe } from '$proto/instance_pb';
import { attachmentText } from './tools';

// Use the page's hostname for wildcard listener addresses.
export function listenerUrl(addr: string, tls: boolean): string {
  const i = addr.lastIndexOf(':');
  let host = i >= 0 ? addr.slice(0, i) : addr;
  const port = i >= 0 ? addr.slice(i) : '';
  if (host === '' || host === '0.0.0.0' || host === '[::]' || host === '::') {
    const here = window.location.hostname.replace(/^\[|\]$/g, '');
    host = here.includes(':') ? `[${here}]` : here;
  }
  return `${tls ? 'https' : 'http'}://${host}${port}`;
}

export interface Dialect {
  id: 'openai' | 'anthropic' | 'ollama';
  label: string;
  // Append to the listener origin for the SDK base URL.
  base: string;
  // Header carrying the API key, with its scheme when one applies
  header: string;
  scheme?: string;
  endpoints: { method: string; path: string }[];
  // Chat request shown as the curl example
  chat: { path: string; headers?: string[]; body: (model: string) => object };
}

export const dialects: Dialect[] = [
  {
    id: 'openai',
    label: 'OpenAI',
    base: '/v1',
    header: 'Authorization',
    scheme: 'Bearer',
    endpoints: [
      { method: 'POST', path: '/v1/chat/completions' },
      { method: 'POST', path: '/v1/completions' },
      { method: 'POST', path: '/v1/embeddings' },
      { method: 'GET', path: '/v1/models' }
    ],
    chat: { path: '/v1/chat/completions', body: (model) => ({ model, messages: [{ role: 'user', content: 'hello' }] }) }
  },
  {
    id: 'anthropic',
    label: 'Anthropic',
    base: '',
    header: 'x-api-key',
    endpoints: [
      { method: 'POST', path: '/v1/messages' },
      { method: 'POST', path: '/v1/messages/count_tokens' },
      { method: 'GET', path: '/v1/models' }
    ],
    chat: { path: '/v1/messages', headers: ['anthropic-version: 2023-06-01'], body: (model) => ({ model, max_tokens: 256, messages: [{ role: 'user', content: 'hello' }] }) }
  },
  {
    id: 'ollama',
    label: 'Ollama',
    base: '',
    header: 'Authorization',
    scheme: 'Bearer',
    endpoints: [
      { method: 'POST', path: '/api/chat' },
      { method: 'POST', path: '/api/generate' },
      { method: 'POST', path: '/api/embed' },
      { method: 'POST', path: '/api/embeddings' },
      { method: 'GET', path: '/api/tags' },
      { method: 'POST', path: '/api/show' },
      { method: 'GET', path: '/api/ps' },
      { method: 'GET', path: '/api/version' }
    ],
    chat: { path: '/api/chat', body: (model) => ({ model, messages: [{ role: 'user', content: 'hello' }], stream: false }) }
  }
];

export const keyHeader = (d: Dialect) => (d.scheme ? `${d.header}: ${d.scheme}` : d.header);

// Double quotes so the shell expands the key variable
export function curl(d: Dialect, origin: string, model: string, auth: boolean): string {
  const key = `${d.header}: ${d.scheme ? `${d.scheme} ` : ''}$API_KEY`;
  const headers = ['Content-Type: application/json', ...(d.chat.headers ?? []), ...(auth ? [key] : [])];
  return [`curl ${origin}${d.chat.path}`, ...headers.map((h) => `-H "${h}"`), `-d '${JSON.stringify(d.chat.body(model))}'`].join(' \\\n  ');
}

const pick = (a: number | undefined, b: number | undefined) => a || b || 0;

// Zero values inherit gateway defaults.
export function policyParts(p: Policy | undefined, d: Policy | undefined): [string, string][] {
  const out: [string, string][] = [];
  const inFlight = pick(p?.maxInFlight, d?.maxInFlight);
  const rps = pick(p?.requestsPerSecond, d?.requestsPerSecond);
  const burst = pick(p?.burst, d?.burst);
  const timeout = pick(p?.requestTimeoutMs, d?.requestTimeoutMs);
  const upstream = pick(p?.upstreamTimeoutMs, d?.upstreamTimeoutMs);
  if (inFlight) out.push(['In flight', String(inFlight)]);
  if (rps) out.push(['Rate', `${rps}/s${burst ? `, burst ${burst}` : ''}`]);
  if (timeout) out.push(['Timeout', `${timeout / 1000} s`]);
  if (upstream) out.push(['First byte', `${upstream / 1000} s`]);
  return out;
}

export function policyText(p: Policy | undefined, d: Policy | undefined): string {
  const parts = policyParts(p, d).map(([k, v]) => `${k.toLowerCase()} ${v}`);
  return parts.length ? parts.join(' · ') : 'No limits';
}

// Empty fields inherit gateway defaults.
export interface PolicyFields {
  maxInFlight: string;
  rps: string;
  burst: string;
  timeout: string;
  upstream: string;
}

export function policyFields(p: Policy | undefined): PolicyFields {
  return {
    maxInFlight: p?.maxInFlight ? String(p.maxInFlight) : '',
    rps: p?.requestsPerSecond ? String(p.requestsPerSecond) : '',
    burst: p?.burst ? String(p.burst) : '',
    timeout: p?.requestTimeoutMs ? String(p.requestTimeoutMs / 1000) : '',
    upstream: p?.upstreamTimeoutMs ? String(p.upstreamTimeoutMs / 1000) : ''
  };
}

const whole = (s: string) => Math.max(0, Math.floor(parseFloat(s) || 0));
const millis = (s: string) => Math.round((parseFloat(s) || 0) * 1000);

export function policyFrom(f: PolicyFields): Policy {
  return { maxInFlight: whole(f.maxInFlight), requestsPerSecond: parseFloat(f.rps) || 0, burst: whole(f.burst), requestTimeoutMs: millis(f.timeout), upstreamTimeoutMs: millis(f.upstream) } as Policy;
}

export function policyCount(f: PolicyFields): number {
  return [f.maxInFlight, f.rps, f.burst, f.timeout, f.upstream].filter((v) => v.trim()).length;
}

export const systemMessageItems: { value: string; label: string; detail: string }[] = [
  { value: 'auto', label: 'Template decides', detail: 'Keep if supported by the template, otherwise merge' },
  { value: 'keep', label: 'Keep', detail: 'Send unchanged' },
  { value: 'merge', label: 'Merge', detail: 'Combine with the first system message' },
  { value: 'user', label: 'User turns', detail: 'Convert to user messages in place' }
];

const modeOf: Record<string, SystemMessages> = { keep: SystemMessages.KEEP, merge: SystemMessages.MERGE, user: SystemMessages.USER };

export const systemPromptItems: { value: string; label: string; detail: string }[] = [
  { value: 'inherit', label: 'Inherit', detail: "The slot's setting, facts for a coding agent and the client's prompt" },
  { value: 'keep', label: 'Keep', detail: "The client's prompt as sent" },
  { value: 'facts', label: 'Facts', detail: "The gateway's own prompt, the environment facts from client" },
  { value: 'drop', label: 'Drop', detail: 'No system prompt reaches the runtime' },
  { value: 'replace', label: 'Replace', detail: "The text below in place of the client's" }
];

const promptOf: Record<string, SystemPrompt> = { keep: SystemPrompt.KEEP, facts: SystemPrompt.FACTS, drop: SystemPrompt.DROP, replace: SystemPrompt.REPLACE };

// Tool patterns as one comma separated line and back
const patternsOf = (s: string) => s.split(',').map((p) => p.trim()).filter(Boolean);

export const checksItems: { value: string; label: string; detail: string }[] = [
  { value: 'inherit', label: 'Inherit', detail: "The slot's setting, allowed when nothing sets one" },
  { value: 'allow', label: 'Allowed', detail: 'Classifier prompts and server side review answered as allowed without the model' },
  { value: 'model', label: 'Model decides', detail: 'The model answers the classifier prompts the agent sends' }
];

export const extrasItems: { value: string; label: string; detail: string }[] = [
  { value: 'inherit', label: 'Inherit', detail: "The slot's setting, stripped for Anthropic clients when nothing sets one" },
  { value: 'strip', label: 'Strip', detail: 'Only what the gateway maps reaches the runtime, no attribution block or identity headers' },
  { value: 'pass', label: 'Pass', detail: 'Every field and header the client sends reaches the runtime' }
];

// A tri state field as its word, inherit when unset
const triState = (v: boolean | undefined, on: string, off: string) => (v === undefined ? 'inherit' : v ? on : off);
const triValue = (s: string, on: string) => (s === 'inherit' ? undefined : s === on);

export interface ProfileFields {
  systemMessages: string;
  checks: string;
  toolServers: string[];
  mutatingTools: boolean;
  systemPrompt: string;
  systemText: string;
  extras: string;
  overrides: string;
  hiddenTools: string;
}

export function profileFields(p: Profile | undefined): ProfileFields {
  return {
    systemMessages: Object.entries(modeOf).find(([, m]) => m === p?.systemMessages)?.[0] ?? 'auto',
    checks: triState(p?.allowAll, 'allow', 'model'),
    toolServers: [...(p?.toolServers ?? [])],
    mutatingTools: p?.mutatingTools ?? false,
    systemPrompt: Object.entries(promptOf).find(([, m]) => m === p?.systemPrompt)?.[0] ?? 'inherit',
    systemText: p?.systemText ?? '',
    extras: triState(p?.stripExtras, 'strip', 'pass'),
    overrides: p?.overrides ?? '',
    hiddenTools: (p?.hiddenTools ?? []).join(', ')
  };
}

export function profileFrom(f: ProfileFields): Profile {
  return {
    systemMessages: modeOf[f.systemMessages] ?? SystemMessages.UNSPECIFIED,
    allowAll: triValue(f.checks, 'allow'),
    toolServers: [...f.toolServers],
    mutatingTools: f.toolServers.length ? f.mutatingTools : false,
    systemPrompt: promptOf[f.systemPrompt] ?? SystemPrompt.UNSPECIFIED,
    systemText: f.systemPrompt === 'replace' ? f.systemText : '',
    stripExtras: triValue(f.extras, 'strip'),
    overrides: f.overrides.trim(),
    hiddenTools: patternsOf(f.hiddenTools)
  } as Profile;
}

// Why the profile cannot be saved as filled in, empty when it can
export function profileError(f: ProfileFields): string {
  if (f.systemPrompt === 'replace' && !f.systemText.trim()) return 'Replacing the system prompt needs text';
  if (f.overrides.trim()) {
    try {
      const parsed = JSON.parse(f.overrides);
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return 'Overrides must be a JSON object';
    } catch {
      return 'Overrides must be a JSON object';
    }
  }
  return '';
}

const modeLabel = (mode: string) => systemMessageItems.find((i) => i.value === mode)?.label.toLowerCase() ?? mode;

function systemText(mode: string, template?: TemplateProbe): string {
  if (mode !== 'auto') return `System messages: ${modeLabel(mode)}`;
  if (!template) return 'System messages: template decides';
  if (template.error) return 'System messages: kept · probe failed';
  return template.lateSystem ? 'System messages: kept · supported by template' : 'System messages: merged · unsupported by template';
}

export function profileText(p: Profile | undefined, template?: TemplateProbe): string {
  const f = profileFields(p);
  const parts = [systemText(f.systemMessages, template)];
  if (f.systemPrompt !== 'inherit') parts.push(`System prompt: ${f.systemPrompt}`);
  if (f.extras !== 'inherit') parts.push(`Client extras: ${f.extras}`);
  if (f.overrides) parts.push('Overrides: set');
  if (patternsOf(f.hiddenTools).length) parts.push(`Hidden tools: ${patternsOf(f.hiddenTools).join(', ')}`);
  if (template?.tools === ToolSupport.EMULATED) parts.push('Tools: emulated by the gateway');
  if (f.checks !== 'inherit') parts.push(f.checks === 'allow' ? 'Agent checks: allowed' : 'Agent checks: model decides');
  const tools = attachmentText(f.toolServers, f.mutatingTools);
  if (tools) parts.push(tools);
  return parts.join(' · ');
}

export function templateText(t: TemplateProbe | undefined): string {
  if (!t) return 'Not probed';
  if (t.error) return `Probe failed: ${t.error}`;
  return t.lateSystem ? 'Supports later system messages' : `Later system messages are unsupported: ${t.refusal}`;
}

// How a runtime takes tools, as the probe found
export function toolSupportText(t: TemplateProbe | undefined, api?: number, sdcpp?: number): string {
  if (api !== undefined && sdcpp !== undefined && api === sdcpp) return 'None, a diffusion model';
  if (!t || t.error) return 'Not probed, definitions pass through as sent';
  switch (t.tools) {
    case ToolSupport.NATIVE:
      return 'Native, the runtime parses tool calls';
    case ToolSupport.EMULATED:
      return `Emulated, the gateway writes tools into the prompt and parses the calls: ${t.toolsRefusal}`;
    case ToolSupport.NONE:
      return 'None';
    default:
      return 'Not probed, definitions pass through as sent';
  }
}

