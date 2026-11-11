<script lang="ts">
	import { goto } from '$app/navigation';
	import { bandwidth, bytes, bytesIn, byteUnit, enumLabel, micros, num, thin, tokensPerSecond, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { RouteState } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { NodeStateSchema, type Node } from '#lib/proto/nebu/v1/mesh_pb.js';
	import type { Storage } from '#lib/proto/nebu/v1/host_pb.js';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import MemoryBars from '#lib/ui/MemoryBars.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import StateWord from '#lib/ui/StateWord.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { deviceLabel } from '#lib/ui/memory.js';
	import { nodeLamp, routeLamp, routeShape } from '#lib/ui/status.js';
	import { poolLamp, poolShape, poolWord } from '#lib/pages/gateway/gateway.js';
	import { flopsRate, linkLabel, linkLamp, linkTip, peerName, streamRate } from '#lib/pages/mesh/mesh.js';

	let { node, tab = $bindable('profile'), onclose }: { node: Node; tab?: string; onclose: () => void } = $props();

	const n = $derived(node);
	const tabs = [
		{ id: 'profile', label: 'Profile' },
		{ id: 'installs', label: 'Installs' },
		{ id: 'stored', label: 'Stored' },
		{ id: 'throughput', label: 'Throughput' },
		{ id: 'links', label: 'Links' },
		{ id: 'serving', label: 'Serving' }
	];

	function deviceName(id: string): string {
		return n.profile?.devices.find((d) => d.id === id)?.name ?? id;
	}

	function usedOverTotal(s: Storage): string {
		return `${bytesIn(s.totalBytes - s.freeBytes, s.totalBytes)} / ${bytesIn(s.totalBytes, s.totalBytes)}${thin}${byteUnit(s.totalBytes)}`;
	}

	function usesOf(s: Storage): string {
		return s.uses.map((u) => u.split(/[\\/]/).filter(Boolean).pop() ?? u).join(', ');
	}
</script>

<PanelHeader title={n.name} lamp={nodeLamp(n.state)} state={enumLabel(NodeStateSchema, n.state)} subtitle={[n.self ? 'this node' : n.address, n.issuer ? 'issuer' : ''].filter(Boolean).join(' · ')} {onclose} />
<Stack gap={16}>
	<PageTabs {tabs} active={tab} onchange={(x) => (tab = x)} />
	{#if tab === 'profile'}
		<Stack gap={24}>
			<MemoryBars profile={n.profile} instances={n.self ? live.instances.values() : []} slots={n.self ? live.slots.values() : []} />
			<Section title="Devices" level={3}>
				{#if n.profile?.devices.length}
					<List head={{ primary: 'Device', secondary: 'Pool', facts: ['Vendor', 'Memory'] }}>
						{#each n.profile.devices as d (d.id)}
							<ListRow
								primary={d.name || d.id}
								primaryTip={[d.id, ...Object.entries(d.facts).map(([k, v]) => `${k} ${v}`)].join('\n')}
								secondary={deviceLabel(n.profile, d)}
								secondaryWidth="8ch"
								facts={[{ value: d.vendor }, { value: bytes(d.memoryTotalBytes || (n.profile?.pools.find((p) => p.id === d.poolId)?.totalBytes ?? 0n)) }]}
							/>
						{/each}
					</List>
				{:else}
					<Empty text="No devices" row />
				{/if}
			</Section>
			<Section title="Storage" level={3}>
				{#if n.profile?.storage.length}
					<List head={{ primary: 'Mount', secondary: 'Filesystem', facts: ['Used', 'Used for'] }}>
						{#each n.profile.storage as s (s.path)}
							<ListRow primary={s.path} primaryKind="path" secondary={s.filesystem} secondaryWidth="8ch" facts={[{ value: usedOverTotal(s), tip: `${bytes(s.freeBytes)} free` }, { value: usesOf(s), tip: s.uses.join('\n') }]} />
						{/each}
					</List>
				{:else}
					<Empty text="No storage" row />
				{/if}
			</Section>
		</Stack>
	{:else if tab === 'installs'}
		{#if n.installs.length}
			<List head={{ primary: 'Runtime', secondary: 'Version' }}>
				{#each n.installs as i (i.id)}
					<ListRow primary={i.runtimeId} primaryTip={i.path} secondary={i.version} secondaryKind="version" secondaryWidth="12ch" />
				{/each}
			</List>
		{:else}
			<Empty text="No installs" />
		{/if}
	{:else if tab === 'stored'}
		{#if n.stored.length}
			<List head={{ primary: 'Model', facts: ['Size'] }}>
				{#each n.stored as m (`${m.sourceId}/${m.repo}/${m.group}`)}
					<ListRow primary={m.repo} primaryKind="repo" primaryTip={[m.repo, m.group, m.sourceId, `pulled ${when(m.pulledAt)}`].filter(Boolean).join('\n')} facts={[{ value: bytes(m.bytes) }]} />
				{/each}
			</List>
		{:else}
			<Empty text="Nothing stored" />
		{/if}
	{:else if tab === 'throughput'}
		{#if n.throughput.length}
			<List head={{ primary: 'Device', secondary: 'Decode', facts: ['Samples', 'Source'] }}>
				{#each n.throughput as t (t.deviceId)}
					<ListRow
						primary={deviceName(t.deviceId)}
						primaryTip={[`stream ${streamRate(t.streamBytesPerSecond)} from ${num(t.streamSamples)}`, `compute ${flopsRate(t.computeFlops)} from ${num(t.computeSamples)}`, `fixed ${micros(t.fixedSeconds * 1e6)}`, `updated ${when(t.updatedAt)}`].join('\n')}
						secondary={tokensPerSecond(t.tokensPerSecond)}
						secondaryWidth="12ch"
						facts={[{ value: num(t.samples) }, { value: t.source }]}
					/>
				{/each}
			</List>
		{:else}
			<Empty text="Nothing measured" />
		{/if}
	{:else if tab === 'links'}
		{#if n.links.length}
			<List head={{ primary: 'To', facts: ['Latency', 'Bandwidth'] }}>
				{#each n.links as l (l.to)}
					<ListRow primary={peerName(live, l.to)} primaryTip={linkTip(l)} facts={[{ value: micros(l.rttUs) }, { value: bandwidth(l.streamBytesPerSecond) }]}>
						{#snippet badges()}<StateWord label={linkLabel(l.class)} lamp={linkLamp(l.class)} />{/snippet}
					</ListRow>
				{/each}
			</List>
		{:else}
			<Empty text="No links measured" />
		{/if}
	{:else}
		{#if n.routes.length === 0 && n.pools.length === 0}
			<Empty text="Nothing serving" />
		{/if}
		<Stack gap={24}>
			{#if n.routes.length}
				<Section title="Routes" level={3}>
					<List head={{ primary: 'Route', secondary: 'Pools' }}>
						{#each n.routes as r (r.name)}
							{@const state = r.status?.state ?? RouteState.UNSPECIFIED}
							<ListRow lamp={routeLamp(state)} shape={routeShape(state)} primary={r.name} secondary={r.status?.poolIds.join(', ') ?? ''} secondaryWidth="24ch" onclick={n.self ? () => goto(`/gateway/routes/${encodeURIComponent(r.name)}`) : undefined} />
						{/each}
					</List>
				</Section>
			{/if}
			{#if n.pools.length}
				<Section title="Pools" level={3}>
					<List head={{ primary: 'Pool', secondary: 'Model', facts: ['State'] }}>
						{#each n.pools as p (p.id)}
							<ListRow lamp={poolLamp(p)} shape={poolShape(p)} primary={p.id} secondary={p.spec?.model ?? ''} secondaryKind="repo" facts={[{ value: poolWord(p) }]} />
						{/each}
					</List>
				</Section>
			{/if}
		</Stack>
	{/if}
</Stack>
