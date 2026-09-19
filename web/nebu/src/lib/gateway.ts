import { SystemMessages, type Policy, type Profile } from '$proto/gateway_pb';
import type { TemplateProbe } from '$proto/instance_pb';

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
  header: string;
  endpoints: { method: string; path: string }[];
  curl: (origin: string, model: string, auth: boolean) => string;
}

export const dialects: Dialect[] = [
  {
    id: 'openai',
    label: 'OpenAI',
    base: '/v1',
    header: 'Authorization: Bearer',
    endpoints: [
      { method: 'POST', path: '/v1/chat/completions' },
      { method: 'POST', path: '/v1/completions' },
      { method: 'POST', path: '/v1/embeddings' },
      { method: 'GET', path: '/v1/models' }
    ],
    curl: (origin, model, auth) =>
      `curl ${origin}/v1/chat/completions \\\n  -H 'Content-Type: application/json' \\\n${auth ? "  -H 'Authorization: Bearer $NEBU_API_KEY' \\\n" : ''}  -d '{"model":"${model}","messages":[{"role":"user","content":"hello"}]}'`
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
    curl: (origin, model, auth) =>
      `curl ${origin}/v1/messages \\\n  -H 'Content-Type: application/json' \\\n  -H 'anthropic-version: 2023-06-01' \\\n${auth ? "  -H 'x-api-key: $NEBU_API_KEY' \\\n" : ''}  -d '{"model":"${model}","max_tokens":256,"messages":[{"role":"user","content":"hello"}]}'`
  },
  {
    id: 'ollama',
    label: 'Ollama',
    base: '',
    header: 'Authorization: Bearer',
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
    curl: (origin, model, auth) =>
      `curl ${origin}/api/chat \\\n  -H 'Content-Type: application/json' \\\n${auth ? "  -H 'Authorization: Bearer $NEBU_API_KEY' \\\n" : ''}  -d '{"model":"${model}","messages":[{"role":"user","content":"hello"}],"stream":false}'`
  }
];

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

export function profileFrom(mode: string): Profile {
  return { systemMessages: modeOf[mode] ?? SystemMessages.UNSPECIFIED } as Profile;
}

export function profileValue(p: Profile | undefined): string {
  return Object.entries(modeOf).find(([, m]) => m === p?.systemMessages)?.[0] ?? 'auto';
}

const modeLabel = (mode: string) => systemMessageItems.find((i) => i.value === mode)?.label.toLowerCase() ?? mode;

export function profileText(p: Profile | undefined, template?: TemplateProbe): string {
  const mode = profileValue(p);
  if (mode !== 'auto') return `System messages: ${modeLabel(mode)}`;
  if (!template) return 'System messages: template decides';
  if (template.error) return 'System messages: kept · probe failed';
  return template.lateSystem ? 'System messages: kept · supported by template' : 'System messages: merged · unsupported by template';
}

export function templateText(t: TemplateProbe | undefined): string {
  if (!t) return 'Not probed';
  if (t.error) return `Probe failed: ${t.error}`;
  return t.lateSystem ? 'Supports later system messages' : `Later system messages are unsupported: ${t.refusal}`;
}
