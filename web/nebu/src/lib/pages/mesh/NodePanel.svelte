<script lang="ts">
	import { bandwidth, bytes, bytesIn, byteUnit, enumLabel, micros, num, thin, tokensPerSecond, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { RouteState } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { NodeStateSchema, type Node } from '#lib/proto/nebu/v1/mesh_pb.js';
	import type { Storage } from '#lib/proto/nebu/v1/host_pb.js';
	import Empty from '#lib/ui/Empty.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import MemoryBars from '#lib/ui/MemoryBars.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { deviceLabel } from '#lib/ui/memory.js';
	import { nodeLamp, routeLamp, routeShape } from '#lib/ui/status.js';
	import { poolLamp, poolShape, poolWord } from '#lib/pages/gateway/gateway.js';
	import { flopsRate, linkLabel, linkLamp, linkTip, peerName, streamRate } from '#lib/pages/mesh/mesh.js';

	let { node, tab = $bindable('profile'), onclose }: { node: Node; tab?: string; onclose: () => void } = $props();

	const n = $derived(node);
	const tabs = $derived([
		{ id: 'profile', label: 'Profile' },
		{ id: 'installs', label: 'Installs', count: n.installs.length },
		{ id: 'stored', label: 'Stored', count: n.stored.length },
		{ id: 'throughput', label: 'Throughput', count: n.throughput.length },
		{ id: 'links', label: 'Links', count: n.links.length },
		{ id: 'serving', label: 'Serving', count: n.routes.length + n.pools.length }
	]);

	function deviceName(id: string): string {
		return n.profile?.devices.find((d) => d.id === id)?.name ?? id;
	}

	// Used over total in the unit of the total, such as 412 / 1024 GB
	function usedOverTotal(s: Storage): string {
		return `${bytesIn(s.totalBytes - s.freeBytes, s.totalBytes)} / ${bytesIn(s.totalBytes, s.totalBytes)}${thin}${byteUnit(s.totalBytes)}`;
	}

	function usesOf(s: Storage): string {
		return s.uses.map((u) => u.split(/[\\/]/).filter(Boolean).pop() ?? u).join(', ');
	}
</script>

<PanelHeader title={n.name} subtitle={n.self ? 'this node' : n.address} {onclose}>
	<StatePill label={enumLabel(NodeStateSchema, n.state)} lamp={nodeLamp(n.state)} />
	{#if n.issuer}<StatePill label="issuer" lamp="primary" />{/if}
</PanelHeader>
<Stack gap={16}>
	<PageTabs {tabs} active={tab} onchange={(x) => (tab = x)} />
	{#if tab === 'profile'}
		<Stack gap={24}>
			<MemoryBars profile={n.profile} instances={n.self ? live.instances.values() : []} slots={n.self ? live.slots.values() : []} />
			<Section title="Devices" count={n.profile?.devices.length ?? 0} gap={0}>
				{#each n.profile?.devices ?? [] as d (d.id)}
					<ListRow
						primary={d.name || d.id}
						primaryTip={[d.id, ...Object.entries(d.facts).map(([k, v]) => `${k} ${v}`)].join('\n')}
						secondary={deviceLabel(n.profile, d)}
						secondaryWidth="8ch"
						facts={[{ value: d.vendor }, { value: d.memoryTotalBytes ? bytes(d.memoryTotalBytes) : 'shared memory' }]}
					/>
				{:else}
					<Empty text="No devices probed" />
				{/each}
			</Section>
			<Section title="Storage" count={n.profile?.storage.length ?? 0} gap={0}>
				{#each n.profile?.storage ?? [] as s (s.path)}
					<ListRow primary={s.path} primaryKind="path" secondary={s.filesystem} secondaryWidth="8ch" facts={[{ value: usedOverTotal(s), tip: `${bytes(s.freeBytes)} free` }, { value: usesOf(s), tip: s.uses.join('\n') }]} />
				{:else}
					<Empty text="No storage probed" />
				{/each}
			</Section>
		</Stack>
	{:else if tab === 'installs'}
		<Stack gap={0}>
			{#each n.installs as i (i.id)}
				<ListRow primary={i.runtimeId} primaryTip={i.path} secondary={i.version || 'unversioned'} secondaryKind="version" secondaryWidth="12ch" />
			{:else}
				<Empty text="No installs" />
			{/each}
		</Stack>
	{:else if tab === 'stored'}
		<Stack gap={0}>
			{#each n.stored as m (`${m.sourceId}/${m.repo}/${m.group}`)}
				<ListRow primary={m.repo} primaryKind="repo" primaryTip={[m.repo, m.group, m.sourceId, `pulled ${when(m.pulledAt)}`].filter(Boolean).join('\n')} facts={[{ value: bytes(m.bytes) }]} />
			{:else}
				<Empty text="Nothing stored" />
			{/each}
		</Stack>
	{:else if tab === 'throughput'}
		<Stack gap={0}>
			{#each n.throughput as t (t.deviceId)}
				<ListRow
					primary={deviceName(t.deviceId)}
					primaryTip={[`stream ${streamRate(t.streamBytesPerSecond)} from ${num(t.streamSamples)}`, `compute ${flopsRate(t.computeFlops)} from ${num(t.computeSamples)}`, `fixed ${micros(t.fixedSeconds * 1e6)}`, `updated ${when(t.updatedAt)}`].join('\n')}
					secondary={tokensPerSecond(t.tokensPerSecond) || 'unmeasured'}
					secondaryWidth="12ch"
					facts={[{ value: `${num(t.samples)} samples` }, { value: t.source || 'learned' }]}
				/>
			{:else}
				<Empty text="Nothing learned" />
			{/each}
		</Stack>
	{:else if tab === 'links'}
		<Stack gap={0}>
			{#each n.links as l (l.to)}
				<ListRow primary={peerName(live, l.to)} primaryTip={linkTip(l)} facts={[{ value: micros(l.rttUs) }, { value: bandwidth(l.streamBytesPerSecond) }]}>
					{#snippet badges()}<StatePill label={linkLabel(l.class)} lamp={linkLamp(l.class)} />{/snippet}
				</ListRow>
			{:else}
				<Empty text="No links measured" />
			{/each}
		</Stack>
	{:else}
		<Stack gap={24}>
			<Section title="Routes" count={n.routes.length} gap={0}>
				{#each n.routes as r (r.name)}
					{@const state = r.status?.state ?? RouteState.UNSPECIFIED}
					<ListRow lamp={routeLamp(state)} shape={routeShape(state)} primary={r.name} secondary={r.status?.poolIds.join(', ') || 'no pool'} secondaryWidth="24ch" facts={n.self ? [{ value: `${num(live.routes.get(r.name)?.status?.inFlight ?? r.status?.inFlight)} in flight` }] : []} />
				{:else}
					<Empty text="No routes" />
				{/each}
			</Section>
			<Section title="Pools" count={n.pools.length} gap={0}>
				{#each n.pools as p (p.id)}
					<ListRow lamp={poolLamp(p)} shape={poolShape(p)} primary={p.id} secondary={p.spec?.model ?? ''} secondaryKind="repo" facts={[{ value: poolWord(p) }]} />
				{:else}
					<Empty text="No pools" />
				{/each}
			</Section>
		</Stack>
	{/if}
</Stack>
