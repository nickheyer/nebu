import type { Policy } from '$proto/gateway_pb';

// Turns a listener address into a URL this browser can reach, an unspecified host meaning the one serving the page
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

// The three wire formats the gateway answers in, each with the base URL clients configure
export interface Dialect {
  id: 'openai' | 'anthropic' | 'ollama';
  label: string;
  // Appended to a listener's origin to make the base URL a client is given
  base: string;
  // The header a key travels in
  header: string;
  // A first request in the dialect
  curl: (origin: string, model: string, auth: boolean) => string;
}

export const dialects: Dialect[] = [
  {
    id: 'openai',
    label: 'OpenAI',
    base: '/v1',
    header: 'Authorization: Bearer',
    curl: (origin, model, auth) =>
      `curl ${origin}/v1/chat/completions \\\n  -H 'Content-Type: application/json' \\\n${auth ? "  -H 'Authorization: Bearer $NEBU_API_KEY' \\\n" : ''}  -d '{"model":"${model}","messages":[{"role":"user","content":"hello"}]}'`
  },
  {
    id: 'anthropic',
    label: 'Anthropic',
    base: '/v1',
    header: 'x-api-key',
    curl: (origin, model, auth) =>
      `curl ${origin}/v1/messages \\\n  -H 'Content-Type: application/json' \\\n  -H 'anthropic-version: 2023-06-01' \\\n${auth ? "  -H 'x-api-key: $NEBU_API_KEY' \\\n" : ''}  -d '{"model":"${model}","max_tokens":256,"messages":[{"role":"user","content":"hello"}]}'`
  },
  {
    id: 'ollama',
    label: 'Ollama',
    base: '',
    header: 'Authorization: Bearer',
    curl: (origin, model, auth) =>
      `curl ${origin}/api/chat \\\n  -H 'Content-Type: application/json' \\\n${auth ? "  -H 'Authorization: Bearer $NEBU_API_KEY' \\\n" : ''}  -d '{"model":"${model}","messages":[{"role":"user","content":"hello"}],"stream":false}'`
  }
];

const pick = (a: number | undefined, b: number | undefined) => a || b || 0;

// The limits a route enforces as label and value pairs, each zero field inheriting the gateway default
export function policyParts(p: Policy | undefined, d: Policy | undefined): [string, string][] {
  const out: [string, string][] = [];
  const inFlight = pick(p?.maxInFlight, d?.maxInFlight);
  const rps = pick(p?.requestsPerSecond, d?.requestsPerSecond);
  const burst = pick(p?.burst, d?.burst);
  const timeout = pick(p?.requestTimeoutMs, d?.requestTimeoutMs);
  const upstream = pick(p?.upstreamTimeoutMs, d?.upstreamTimeoutMs);
  if (inFlight) out.push(['in flight', String(inFlight)]);
  if (rps) out.push(['per second', `${rps}${burst ? ` burst ${burst}` : ''}`]);
  if (timeout) out.push(['timeout', `${timeout / 1000}s`]);
  if (upstream) out.push(['first byte', `${upstream / 1000}s`]);
  return out;
}

// The same limits in one line
export function policyText(p: Policy | undefined, d: Policy | undefined): string {
  const parts = policyParts(p, d).map(([k, v]) => (k === 'in flight' ? `${v} in flight` : k === 'per second' ? `${v}/s` : `${v} ${k}`));
  return parts.length ? parts.join(' · ') : 'none';
}
