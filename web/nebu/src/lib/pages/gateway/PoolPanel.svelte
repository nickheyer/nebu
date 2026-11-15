<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { num, pct } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { Pool } from '#lib/proto/nebu/v1/pool_pb.js';
	import Bar from '#lib/ui/Bar.svelte';
	import Conditions from '#lib/ui/Conditions.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { endpointState, endpointTip, exported, failureLabel, isUserOwned, ownerLabel, protocolLabel } from '#lib/pages/gateway/gateway.js';

	let { pool, onclose }: { pool: Pool; onclose: () => void } = $props();

	const endpoints = $derived(pool.status?.endpoints ?? []);
	const spec = $derived(pool.spec);
</script>

<PanelHeader title={pool.id} subtitle={spec?.model} {onclose}>
	{#snippet tools()}
		{#if isUserOwned(pool.ownerKind)}<NeoButton elevation={2} href="/gateway/pools/{encodeURIComponent(pool.id)}?edit=1">Edit</NeoButton>{/if}
	{/snippet}
</PanelHeader>
<Stack gap={24}>
	<Section title="Endpoints" level={3}>
		{#if endpoints.length}
			<List head={{ primary: 'Address', instrument: 'Cache in use', facts: ['State', 'Queued', 'Running'] }}>
				{#each endpoints as e (e.id)}
					{@const kv = e.metrics?.kvCacheUtilization ?? 0}
					{@const state = endpointState(e)}
					<ListRow lamp={state.lamp} shape={state.shape} primary={e.address} primaryKind="address" primaryTip={endpointTip(live, e, clockTick.now)} facts={[{ value: state.word }, { value: e.metrics ? num(e.metrics.queuedRequests) : '\u2014' }, { value: e.metrics ? num(e.metrics.runningRequests) : '\u2014' }]}>
						{#snippet instrument()}
							{#if e.metrics}
								<span class="kv"><Bar segments={[{ kind: kv >= 0.9 ? 'warning' : 'used', label: 'Cache in use', bytes: kv * 100 }]} total={100} format={(n) => pct(n)} height={6} /></span>
								<span class="secondary num">{pct(kv * 100)}</span>
							{:else}
								<span class="secondary">no metrics read</span>
							{/if}
						{/snippet}
					</ListRow>
				{/each}
			</List>
		{:else}
			<Empty text="No endpoints" row />
		{/if}
	</Section>
	{#if pool.status?.conditions.length}
		<Section title="Checks" level={3}>
			<Conditions conditions={pool.status.conditions} />
		</Section>
	{/if}
	<Section title="Settings" level={3}>
		<KeyRows
			dense
			rows={[
				{ label: 'owner', value: ownerLabel(pool.ownerKind, pool.ownerId) },
				{ label: 'runtime', value: spec?.runtimeId ?? '' },
				{ label: 'accelerator', value: spec?.accelerator ?? '', tip: spec?.accelerator || undefined },
				{ label: 'selector', value: Object.entries(spec?.selector ?? {}).map(([k, v]) => `${k}=${v}`).join(' '), mono: true },
				{ label: 'ports', value: spec?.targetPorts.join(', ') ?? '', mono: true },
				{ label: 'protocol', value: protocolLabel(spec?.appProtocol) },
				{ label: 'if the scheduler fails', value: failureLabel(spec?.failureMode) },
				{ label: 'scheduler', value: spec?.schedulerConfig ?? '', href: spec?.schedulerConfig ? `/gateway/schedulers/${encodeURIComponent(spec.schedulerConfig)}` : undefined },
				{ label: 'concurrency', value: spec?.concurrency ? num(spec.concurrency) : '' },
				{ label: 'send token ids', value: spec?.tokensInOut ? 'on' : 'off' },
				{ label: 'export', value: exported(pool) ? 'on' : 'off' },
				{ label: 'context', value: spec?.inventory?.contextLength ? `${num(spec.inventory.contextLength)} tokens` : '' },
				{ label: 'quantization', value: spec?.inventory?.quantization ?? '' },
				{ label: 'modalities', value: spec?.inventory?.modalities.join(', ') ?? '' },
				{ label: 'static endpoints', value: spec?.staticEndpoints.map((s) => s.address).join(', ') ?? '', mono: true }
			]}
		/>
	</Section>
</Stack>

<style>
	.kv {
		display: flex;
		flex: 1 1 auto;
		min-width: 4rem;
		margin-right: var(--nebu-space-2);
	}
</style>
