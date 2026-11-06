<script lang="ts">
	import { onMount } from 'svelte';
	import { timestampFromDate } from '@bufbuild/protobuf/wkt';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, date, num, pct, perMinute, ms } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { Trace } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { RouteState } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { InstanceState } from '#lib/proto/nebu/v1/instance_pb.js';
	import { TaskState } from '#lib/proto/nebu/v1/task_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import Facts, { type FactItem } from '#lib/ui/Facts.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import MemoryBars from '#lib/ui/MemoryBars.svelte';
	import ShowAll from '#lib/ui/ShowAll.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Split from '#lib/ui/layout/Split.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { routeLamp, routeShape } from '#lib/ui/status.js';
	import { attention } from '#lib/pages/overview/attention.js';

	const window = 5 * 60_000;
	const bucket = 5_000;

	let seeded = $state<Trace[]>([]);
	let baseline = $state<bigint | undefined>(undefined);
	let allAttention = $state(false);
	let allServing = $state(false);

	onMount(async () => {
		try {
			const [traces, status] = await Promise.all([
				rpc.gateway.listTraces({ from: timestampFromDate(new Date(Date.now() - window)), limit: 1000 }),
				rpc.gateway.getGatewayStatus({})
			]);
			seeded = traces.traces;
			baseline = status.status?.requests;
		} catch (err) {
			toast.error('Overview failed to load', reason(err));
		}
	});

	// Recent traces by id, the live stream winning over the seed
	const recent = $derived.by(() => {
		const now = clockTick.now;
		const byId = new Map<string, Trace>();
		for (const t of seeded) byId.set(t.id, t);
		for (const t of live.traces) byId.set(t.id, t);
		return [...byId.values()].filter((t) => now - (date(t.startedAt)?.getTime() ?? 0) <= window);
	});
	const lastMinute = $derived(recent.filter((t) => clockTick.now - (date(t.startedAt)?.getTime() ?? 0) <= 60_000).length);
	const spark = $derived.by(() => {
		const now = clockTick.now;
		const buckets = new Array<number>(window / bucket).fill(0);
		for (const t of recent) {
			const age = now - (date(t.startedAt)?.getTime() ?? now);
			const i = buckets.length - 1 - Math.floor(age / bucket);
			if (i >= 0 && i < buckets.length) buckets[i]!++;
		}
		return buckets;
	});
	const finished = $derived(recent.filter((t) => t.finishedAt));
	const errorRate = $derived(finished.length ? (finished.filter((t) => t.status >= 400 || t.error).length / finished.length) * 100 : 0);
	const ttftP50 = $derived.by(() => {
		const list = recent
			.filter((t) => t.firstTokenAt && t.startedAt)
			.map((t) => (date(t.firstTokenAt)?.getTime() ?? 0) - (date(t.startedAt)?.getTime() ?? 0))
			.sort((a, b) => a - b);
		return list.length ? list[Math.floor(list.length / 2)] : undefined;
	});

	const routes = $derived([...live.routes.values()].sort((a, b) => Number(b.status?.state === RouteState.READY) - Number(a.status?.state === RouteState.READY) || a.name.localeCompare(b.name)));
	const readyRoutes = $derived(routes.filter((r) => r.status?.state === RouteState.READY).length);
	const pendingRoutes = $derived(routes.filter((r) => r.status?.state === RouteState.PENDING).length);
	const failedInstances = $derived([...live.instances.values()].filter((i) => i.state === InstanceState.FAILED).length);
	const active = $derived(live.activeTasks);
	const failedTasks = $derived([...live.tasks.values()].filter((t) => t.state === TaskState.FAILED && clockTick.now - (date(t.finishedAt)?.getTime() ?? 0) < 3_600_000).length);
	const attn = $derived(attention(live, clockTick.now));
	const store = $derived(live.store);
	const storeUsed = $derived(store ? Number(store.blobBytes + store.partialBytes + store.cacheBytes) : 0);
	const storeLimit = $derived(store ? Number(store.maxBytes) : 0);

	const facts = $derived.by((): FactItem[] => [
		{
			id: 'serving',
			label: 'Serving',
			value: `${num(readyRoutes)} ready`,
			href: '/serve',
			lamp: failedInstances ? 'error' : pendingRoutes ? 'warning' : undefined,
			dots: routes.map((r) => routeLamp(r.status?.state ?? RouteState.UNSPECIFIED)),
			facts: [
				{ label: 'pending', value: num(pendingRoutes) },
				{ label: 'failed', value: num(failedInstances) }
			]
		},
		{
			id: 'requests',
			label: 'Requests',
			value: perMinute(lastMinute),
			href: '/traffic',
			spark,
			facts: [
				{ label: 'error rate', value: pct(errorRate, 1) },
				{ label: 'TTFT p50', value: ttftP50 === undefined ? 'none' : ms(ttftP50) },
				{ label: 'total', value: baseline === undefined ? '' : num(baseline) }
			]
		},
		{
			id: 'attention',
			label: 'Attention',
			value: num(attn.length),
			href: attn[0]?.href ?? '/serve',
			lamp: attn.some((a) => a.lamp === 'error') ? 'error' : attn.length ? 'warning' : undefined,
			facts: [
				{ label: 'failures', value: num(attn.filter((a) => a.lamp === 'error').length) },
				{ label: 'warnings', value: num(attn.filter((a) => a.lamp === 'warning').length) }
			]
		},
		{
			id: 'tasks',
			label: 'Tasks',
			value: `${num(active.length)} active`,
			href: '/tasks',
			lamp: failedTasks ? 'error' : undefined,
			facts: [{ label: 'failed in the last hour', value: num(failedTasks) }]
		},
		{
			id: 'store',
			label: 'Store',
			value: storeLimit > 0 ? pct((storeUsed / storeLimit) * 100) : bytes(storeUsed),
			href: '/models',
			lamp: storeLimit > 0 && storeUsed > storeLimit ? 'warning' : undefined,
			fraction: storeLimit > 0 ? storeUsed / storeLimit : undefined,
			facts: [
				{ label: 'used', value: bytes(storeUsed) },
				{ label: 'limit', value: storeLimit > 0 ? bytes(storeLimit) : 'none' }
			]
		}
	]);

	const firstRun = $derived(live.ready && live.installs.size === 0 && live.models.size === 0 && live.instances.size === 0);
	const steps = $derived([
		{ id: 'install', label: 'Install a runtime', done: live.installs.size > 0, action: 'Install', href: '/runtimes' },
		{ id: 'pull', label: 'Pull a model', done: live.models.size > 0, action: 'Discover', href: '/discover' },
		{ id: 'launch', label: 'Launch', done: live.instances.size > 0, action: 'Launch', href: '/serve/launch' }
	]);

	function modelOf(poolIds: string[]): string {
		for (const id of poolIds) {
			const model = live.pools.get(id)?.spec?.model;
			if (model) return model;
		}
		return '';
	}
</script>

{#snippet firstRunSection()}
	<Section title="First run">
		{#each steps as s (s.id)}
			<ListRow lamp={s.done ? 'success' : 'muted'} shape={s.done ? 'filled' : 'hollow'} primary={s.label}>
				{#snippet action()}<NeoButton text href={s.href} disabled={s.done}>{s.action}</NeoButton>{/snippet}
			</ListRow>
		{/each}
	</Section>
{/snippet}

{#snippet attentionSection()}
	<Section title="Attention" count={attn.length}>
		{#if !live.ready}
			<Skeleton rows={2} />
		{:else if attn.length === 0}
			<Empty text="All clear" />
		{:else}
			{#each allAttention ? attn : attn.slice(0, 8) as a (a.id)}
				<ListRow lamp={a.lamp} primary={a.title} secondary={a.reason} secondaryKind="message" secondaryWidth="64ch" href={a.href}>
					{#snippet action()}<NeoButton text href={a.href}>Open</NeoButton>{/snippet}
				</ListRow>
			{/each}
			<ShowAll total={attn.length} shown={8} bind:open={allAttention} />
		{/if}
	</Section>
{/snippet}

{#snippet servingSection()}
	<Section title="Serving" count={routes.length}>
		{#if !live.ready}
			<Skeleton rows={3} />
		{:else if routes.length === 0}
			<Empty text="Nothing serving" action="Launch" href="/serve/launch" />
		{:else}
			{#each allServing ? routes : routes.slice(0, 8) as r (r.name)}
				{@const state = r.status?.state ?? RouteState.UNSPECIFIED}
				<ListRow
					lamp={routeLamp(state)}
					shape={routeShape(state)}
					primary={r.name}
					secondary={state === RouteState.PENDING ? 'starting' : modelOf(r.status?.poolIds ?? [])}
					secondaryKind="repo"
					facts={state === RouteState.READY ? [{ value: `${num(r.status?.inFlight)} in flight` }] : []}
					href="/gateway/routes/{encodeURIComponent(r.name)}"
				>
					{#snippet action()}<NeoButton text href="/gateway/routes/{encodeURIComponent(r.name)}">Open</NeoButton>{/snippet}
				</ListRow>
			{/each}
			<ShowAll total={routes.length} shown={8} bind:open={allServing} />
		{/if}
	</Section>
{/snippet}

{#snippet memorySection()}
	<Section title="Memory">
		{#if !live.ready}
			<Skeleton rows={3} height={28} />
		{:else}
			<MemoryBars profile={live.host} instances={live.instances.values()} slots={live.slots.values()} />
		{/if}
	</Section>
{/snippet}

<Stack gap={24}>
	{#if live.ready}
		{#if !firstRun}<Facts items={facts} />{/if}
	{:else}
		<Skeleton rows={1} height={44} />
	{/if}
	{#if viewport.ultrawide}
		<Split ratio="3/5">
			{#snippet left()}
				{#if firstRun}
					{@render firstRunSection()}
				{:else}
					{@render attentionSection()}
					{@render servingSection()}
				{/if}
			{/snippet}
			{#snippet right()}
				{@render memorySection()}
			{/snippet}
		</Split>
	{:else if firstRun}
		{@render firstRunSection()}
		{@render memorySection()}
	{:else}
		{@render attentionSection()}
		{@render memorySection()}
		{@render servingSection()}
	{/if}
</Stack>
