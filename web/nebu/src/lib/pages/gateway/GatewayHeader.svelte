<script lang="ts">
	import type { Snippet } from 'svelte';
	import { live } from '#lib/live/events.svelte.js';
	import Bone from '#lib/ui/Bone.svelte';
	import InfoTip from '#lib/ui/InfoTip.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import { listenersTip, shortAddr, statusFacts } from '#lib/pages/gateway/gateway.js';
	import type { GatewayGroup } from '#lib/pages/gateway/group.svelte.js';
	import { gatewayHeaderTabs, type GroupTab } from '#lib/pages/gateway/tabs.js';

	// The header every Gateway tab shares, the listener and auth after the title and all six counts once each is known
	let { group, active, traffic, actions }: { group: GatewayGroup; active: GroupTab; traffic: number | undefined; actions?: Snippet } = $props();

	const counts = $derived(
		live.ready && group.importsLoaded && traffic !== undefined
			? { traffic, routes: live.routes.size, pools: live.pools.size, objectives: live.objectives.size, schedulers: live.schedulerConfigs.size, imports: group.imports.length }
			: undefined
	);
	const tabs = $derived(gatewayHeaderTabs(counts));
</script>

{#snippet meta()}
	{#if group.status}
		{@const s = group.status}
		<Tip text={listenersTip(s) || undefined} tag="span">
			<span><span class="mono">{s.listeners[0] ? shortAddr(s.listeners[0].addr) : 'no listener'}</span> {s.tls ? 'TLS' : 'plain'}</span>
		</Tip>
		<span class:warning-text={!s.auth}>auth {s.auth ? 'on' : 'off'}</span>
		<InfoTip click facts={statusFacts(s)} />
	{:else}
		<Bone width="22ch" height="14px" />
	{/if}
{/snippet}

<PageHeader {tabs} {active} {meta} {actions} />
