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
