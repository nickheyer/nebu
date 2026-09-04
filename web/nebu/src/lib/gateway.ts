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

// Words for the limits a route enforces, each zero field of its policy inheriting the gateway default
export function policyText(p: Policy | undefined, d: Policy | undefined): string {
  const pick = (a: number | undefined, b: number | undefined) => a || b || 0;
  const parts: string[] = [];
  const inFlight = pick(p?.maxInFlight, d?.maxInFlight);
  const rps = pick(p?.requestsPerSecond, d?.requestsPerSecond);
  const burst = pick(p?.burst, d?.burst);
  const timeout = pick(p?.requestTimeoutMs, d?.requestTimeoutMs);
  const upstream = pick(p?.upstreamTimeoutMs, d?.upstreamTimeoutMs);
  if (inFlight) parts.push(`${inFlight} in flight`);
  if (rps) parts.push(`${rps}/s${burst ? ` burst ${burst}` : ''}`);
  if (timeout) parts.push(`${timeout / 1000}s total`);
  if (upstream) parts.push(`${upstream / 1000}s first byte`);
  return parts.length ? parts.join(', ') : 'none';
}
