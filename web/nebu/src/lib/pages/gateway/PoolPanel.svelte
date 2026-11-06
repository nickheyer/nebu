<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import Collapse from '#lib/ui/Collapse.svelte';
	import { enumLabel, num, pct } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { AppProtocolSchema, FailureModeSchema, type Pool } from '#lib/proto/nebu/v1/pool_pb.js';
	import Bar from '#lib/ui/Bar.svelte';
	import Conditions from '#lib/ui/Conditions.svelte';
	import Dot from '#lib/ui/Dot.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { endpointTip, exported, isUserOwned, ownerLabel } from '#lib/pages/gateway/gateway.js';

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
	<Section title="Endpoints" count="{num(pool.status?.readyEndpoints)} / {num(pool.status?.totalEndpoints)}" gap={0}>
		{#each endpoints as e (e.id)}
			{@const kv = e.metrics?.kvCacheUtilization ?? 0}
			<ListRow
				primary={e.address}
				primaryKind="address"
				primaryTip={endpointTip(live, e, clockTick.now)}
				facts={[{ value: `${num(e.metrics?.queuedRequests)} queued` }, { value: `${num(e.metrics?.runningRequests)} running` }]}
			>
				{#snippet leading()}
					<Tip text={e.ready ? 'Ready' : 'Not ready'} tag="span"><Dot lamp={e.ready ? 'success' : 'muted'} shape={e.ready ? 'filled' : 'hollow'} label="ready" /></Tip>
					<Tip text={e.healthy ? 'Healthy' : 'Unhealthy'} tag="span"><Dot lamp={e.healthy ? 'success' : 'error'} label="healthy" /></Tip>
				{/snippet}
				{#snippet action()}
					<span class="kv"><Bar segments={[{ kind: kv >= 0.9 ? 'warning' : 'used', label: 'KV cache', bytes: kv * 100 }]} total={100} format={(n) => pct(n)} height={6} /></span>
				{/snippet}
			</ListRow>
		{:else}
			<Empty text="No endpoints" />
		{/each}
	</Section>
	<Section title="Conditions">
		<Conditions conditions={pool.status?.conditions} />
	</Section>
	<Collapse label="Spec">
		<KeyRows
			dense
			rows={[
				{ label: 'owner', value: ownerLabel(pool.ownerKind, pool.ownerId) },
				{ label: 'runtime', value: spec?.runtimeId || 'any' },
				{ label: 'accelerator', value: spec?.accelerator || 'any', tip: spec?.accelerator || undefined },
				{ label: 'selector', value: Object.entries(spec?.selector ?? {}).map(([k, v]) => `${k}=${v}`).join(' '), mono: true },
				{ label: 'target ports', value: spec?.targetPorts.join(', ') ?? '', mono: true },
				{ label: 'protocol', value: enumLabel(AppProtocolSchema, spec?.appProtocol ?? 0) },
				{ label: 'failure mode', value: enumLabel(FailureModeSchema, spec?.failureMode ?? 0) },
				{ label: 'scheduler', value: spec?.schedulerConfig || 'built in', href: spec?.schedulerConfig ? `/gateway/schedulers/${encodeURIComponent(spec.schedulerConfig)}` : undefined },
				{ label: 'concurrency', value: spec?.concurrency ? num(spec.concurrency) : 'runtime reported' },
				{ label: 'tokens in and out', value: spec?.tokensInOut ? 'on' : 'off' },
				{ label: 'export', value: exported(pool) ? 'on' : 'off' },
				{ label: 'context', value: spec?.inventory?.contextLength ? `${num(spec.inventory.contextLength)} tokens` : '' },
				{ label: 'quantization', value: spec?.inventory?.quantization ?? '' },
				{ label: 'modalities', value: spec?.inventory?.modalities.join(', ') ?? '' },
				{ label: 'static endpoints', value: spec?.staticEndpoints.map((s) => s.address).join(', ') ?? '', mono: true }
			]}
		/>
	</Collapse>
</Stack>

<style>
	.kv {
		display: flex;
		flex: 0 0 6rem;
		min-width: 6rem;
		max-width: 6rem;
	}
</style>
