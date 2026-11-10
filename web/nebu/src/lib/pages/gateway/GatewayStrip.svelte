<script lang="ts">
	import { ms, num } from '#lib/format.js';
	import type { GatewayStatus } from '#lib/proto/nebu/v1/gateway_pb.js';
	import Facts, { type FactItem } from '#lib/ui/Facts.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import { compact, listenersTip, shortAddr } from '#lib/pages/gateway/gateway.js';

	let { status }: { status: GatewayStatus | undefined } = $props();

	const infinity = '\u221e';

	// Listener, transport, auth, requests served and the fallback policy
	const items = $derived.by((): FactItem[] => {
		const s = status;
		if (!s) return [];
		const p = s.policy;
		return [
			{ id: 'listener', label: 'Listener', value: s.listeners[0] ? shortAddr(s.listeners[0].addr) : '', mono: true, tip: listenersTip(s) || undefined, lamp: s.listeners.length ? undefined : 'error' },
			{ id: 'tls', label: 'Transport', value: s.tls ? 'TLS' : 'plain', lamp: s.tls ? 'success' : 'warning' },
			{ id: 'auth', label: 'Auth', value: s.auth ? 'on' : 'off', lamp: s.auth ? 'success' : 'warning' },
			{ id: 'requests', label: 'Requests', value: compact(s.requests), tip: num(s.requests) },
			{ id: 'inflight', label: 'Concurrent requests', value: p?.maxInFlight ? num(p.maxInFlight) : infinity },
			{ id: 'rate', label: 'Rate', value: p?.requestsPerSecond ? `${p.requestsPerSecond}/s` : infinity, tip: p?.burst ? `burst ${num(p.burst)}` : undefined },
			{ id: 'timeout', label: 'Timeout', value: p?.requestTimeoutMs ? ms(p.requestTimeoutMs) : infinity, tip: p?.upstreamTimeoutMs ? `upstream ${ms(p.upstreamTimeoutMs)}` : undefined },
			{ id: 'version', label: 'Version', value: `${s.implementation} ${s.version}`.trim(), mono: true, tip: s.metricsAddr ? `metrics ${s.metricsAddr}` : undefined }
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
