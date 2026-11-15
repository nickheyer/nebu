<script lang="ts">
	import { ms, num } from '#lib/format.js';
	import type { GatewayStatus } from '#lib/proto/nebu/v1/gateway_pb.js';
	import Facts, { type FactItem } from '#lib/ui/Facts.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import { compact, listenersTip, shortAddr } from '#lib/pages/gateway/gateway.js';

	let { status }: { status: GatewayStatus | undefined } = $props();

	// Where the gateway listens and how, what it has served, then the limits it holds every route to, each named as a limit
	const items = $derived.by((): FactItem[] => {
		const s = status;
		if (!s) return [];
		const p = s.policy;
		const addr = s.listeners[0] ? shortAddr(s.listeners[0].addr) : '';
		const port = /:\d+$/.exec(addr)?.[0] ?? '';
		const more = s.listeners.length - 1;
		return [
			{ id: 'listener', label: 'Listener', value: addr, mono: true, wide: true, keep: port.length, note: more > 0 ? `and ${more} more` : undefined, tip: listenersTip(s) || undefined, lamp: s.listeners.length ? undefined : 'error' },
			{ id: 'tls', label: 'Transport', value: s.tls ? 'TLS' : 'HTTP', note: s.tls ? 'encrypted' : 'not encrypted', lamp: s.tls ? 'success' : 'warning' },
			{ id: 'auth', label: 'Sign in', value: s.auth ? 'required' : 'off', note: s.auth ? 'every request needs a token' : 'anyone who reaches it may send', lamp: s.auth ? 'success' : 'warning' },
			{ id: 'requests', label: 'Requests served', value: compact(s.requests), note: 'since the gateway started', tip: num(s.requests) },
			{ id: 'inflight', label: 'Concurrency limit', value: p?.maxInFlight ? num(p.maxInFlight) : 'none', quiet: !p?.maxInFlight, note: p?.maxInFlight ? 'requests at once' : undefined },
			{ id: 'rate', label: 'Rate limit', value: p?.requestsPerSecond ? `${p.requestsPerSecond} /s` : 'none', quiet: !p?.requestsPerSecond, note: p?.burst ? `bursts of ${num(p.burst)}` : undefined },
			{ id: 'timeout', label: 'Request timeout', value: p?.requestTimeoutMs ? ms(p.requestTimeoutMs) : 'none', quiet: !p?.requestTimeoutMs, note: p?.upstreamTimeoutMs ? `the runtime gets ${ms(p.upstreamTimeoutMs)}` : undefined }
		];
	});
</script>

<Card>
	{#if !status}
		<Skeleton rows={1} kind="strip" />
	{:else}
		<Facts items={items} />
	{/if}
</Card>
