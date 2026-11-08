<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { timestampFromDate } from '@bufbuild/protobuf/wkt';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { ago, bytes, date, enumLabel, num, pct, perMinute, plural, ms } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { Trace } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { RouteState } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { InstanceState } from '#lib/proto/nebu/v1/instance_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { TaskState, TaskStateSchema, type Task } from '#lib/proto/nebu/v1/task_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Dot from '#lib/ui/Dot.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import Mark, { type MarkState } from '#lib/ui/Mark.svelte';
	import MemoryBars from '#lib/ui/MemoryBars.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import ShowAll from '#lib/ui/ShowAll.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import Columns from '#lib/ui/layout/Columns.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { routeLamp, routeShape } from '#lib/ui/status.js';
	import { attention } from '#lib/pages/overview/attention.js';
	import Pipeline, { type Stage } from '#lib/pages/overview/Pipeline.svelte';
	import { active as taskActive } from '#lib/pages/tasks/tasks.js';
	import PlayIcon from 'phosphor-svelte/lib/PlayIcon';

	const window = 5 * 60_000;
	const bucket = 10_000;

	let seeded = $state<Trace[]>([]);
	let runtimes = $state<RuntimeStatus[]>([]);
	let allServing = $state(false);
	let allAttention = $state(false);

	onMount(async () => {
		const [traces, list] = await Promise.allSettled([rpc.gateway.listTraces({ from: timestampFromDate(new Date(Date.now() - window)), limit: 1000 }), rpc.runtimes.listRuntimes({})]);
		if (traces.status === 'fulfilled') seeded = traces.value.traces;
		else toast.error('Recent requests failed to load', reason(traces.reason));
		if (list.status === 'fulfilled') runtimes = list.value.runtimes;
		else toast.error('Runtimes failed to list', reason(list.reason));
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
	const instances = $derived([...live.instances.values()]);
	const runningInstances = $derived(instances.filter((i) => i.state === InstanceState.READY || i.state === InstanceState.IDLE || i.state === InstanceState.DRAINING).length);
	const startingInstances = $derived(instances.filter((i) => i.state === InstanceState.STARTING).length);
	const failedInstances = $derived(instances.filter((i) => i.state === InstanceState.FAILED).length);
	const attn = $derived(attention(live, clockTick.now));
	const store = $derived(live.store);
	const storeUsed = $derived(store ? Number(store.blobBytes + store.partialBytes + store.cacheBytes) : 0);
	const installed = $derived.by(() => {
		const ids = new Set([...live.installs.values()].map((i) => i.runtimeId));
		return runtimes.filter((r) => ids.has(r.runtime?.id ?? '')).map((r) => r.runtime?.name ?? '');
	});
	const compatible = $derived(runtimes.filter((r) => r.compatible).length);
	const recentTasks = $derived([...live.tasks.values()].sort((a, b) => Number(b.createdAt?.seconds ?? 0n) - Number(a.createdAt?.seconds ?? 0n)).slice(0, 6));

	const stages = $derived.by((): Stage[] => [
		{
			id: 'runtimes',
			label: 'Runtimes',
			value: live.installs.size ? `${num(live.installs.size)} installed` : 'None installed',
			detail: installed.length ? installed.join(', ') : `${num(compatible)} compatible with this host`,
			href: '/runtimes',
			done: live.installs.size > 0,
			lamp: live.installs.size ? 'success' : undefined,
			action: { label: 'Install a runtime', href: '/runtimes' }
		},
		{
			id: 'models',
			label: 'Models',
			value: live.models.size ? `${num(live.models.size)} stored` : 'None stored',
			detail: live.models.size ? `${bytes(storeUsed)} on disk` : 'Pull one from a source',
			href: '/models',
			done: live.models.size > 0,
			lamp: live.models.size ? 'success' : undefined,
			action: { label: 'Discover models', href: '/discover' }
		},
		{
			id: 'instances',
			label: 'Instances',
			value: instances.length ? `${num(runningInstances)} running` : 'None running',
			detail: [startingInstances ? `${num(startingInstances)} starting` : '', failedInstances ? `${num(failedInstances)} failed` : ''].filter(Boolean).join(' · ') || (instances.length ? 'All healthy' : 'Launch a model on a runtime'),
			href: '/serve',
			done: instances.length > 0,
			lamp: failedInstances ? 'error' : startingInstances ? 'warning' : runningInstances ? 'success' : undefined,
			action: { label: 'Launch', href: '/serve/launch' }
		},
		{
			id: 'routes',
			label: 'Routes',
			value: routes.length ? `${num(readyRoutes)} ready` : 'None',
			detail: routes.length ? (pendingRoutes ? `${num(pendingRoutes)} pending` : `${plural(routes.length, 'route')} in the gateway`) : 'Each instance gets one',
			href: '/gateway/routes',
			done: readyRoutes > 0,
			lamp: pendingRoutes ? 'warning' : readyRoutes ? 'success' : undefined
		},
		{
			id: 'traffic',
			label: 'Traffic',
			value: perMinute(lastMinute),
			detail: finished.length ? `${pct(errorRate, 1)} errors${ttftP50 === undefined ? '' : ` · TTFT ${ms(ttftP50)}`}` : 'No requests in five minutes',
			href: '/traffic',
			done: recent.length > 0,
			lamp: errorRate > 5 ? 'error' : errorRate > 0 ? 'warning' : recent.length ? 'success' : undefined,
			spark
		}
	]);

	function modelOf(poolIds: string[]): string {
		for (const id of poolIds) {
			const model = live.pools.get(id)?.spec?.model;
			if (model) return model;
		}
		return '';
	}

	function markOf(t: Task): MarkState {
		if (t.state === TaskState.SUCCEEDED) return 'ok';
		if (t.state === TaskState.FAILED) return 'failed';
		if (taskActive(t)) return 'active';
		return 'skipped';
	}
</script>

{#snippet attentionCard()}
	<Card title="Needs attention" count={attn.length} flush>
		<List head={{ primary: 'Resource', secondary: 'Reason' }}>
			{#each allAttention ? attn : attn.slice(0, 8) as a (a.id)}
				<ListRow lamp={a.lamp} primary={a.title} secondary={a.reason} secondaryKind="message" secondaryWidth="64ch" onclick={() => goto(a.href)} />
			{/each}
		</List>
		<ShowAll total={attn.length} shown={8} bind:open={allAttention} />
	</Card>
{/snippet}

{#snippet servingCard()}
	<Card title="Serving" count={live.ready ? routes.length : undefined} flush>
		{#snippet tools()}<RowAction label="Gateway" href="/gateway/routes" />{/snippet}
		{#if !live.ready}
			<Skeleton rows={3} secondary="40ch" menu={false} action={false} />
		{:else if routes.length === 0}
			<Empty icon={PlayIcon} text="Nothing serving" hint="Launch a model and the gateway answers it under a route" action="Launch" href="/serve/launch" />
		{:else}
			<List head={{ primary: 'Route', secondary: 'Model', facts: ['In flight'] }}>
				{#each allServing ? routes : routes.slice(0, 8) as r (r.name)}
					{@const state = r.status?.state ?? RouteState.UNSPECIFIED}
					<ListRow
						lamp={routeLamp(state)}
						shape={routeShape(state)}
						primary={r.name}
						secondary={state === RouteState.PENDING ? 'starting' : modelOf(r.status?.poolIds ?? [])}
						secondaryKind="repo"
						facts={[{ value: state === RouteState.READY ? num(r.status?.inFlight) : '' }]}
						onclick={() => goto(`/gateway/routes/${encodeURIComponent(r.name)}`)}
					/>
				{/each}
			</List>
			<ShowAll total={routes.length} shown={8} bind:open={allServing} />
		{/if}
	</Card>
{/snippet}

{#snippet memoryCard()}
	<Card title="Memory">
		{#snippet tools()}<RowAction label="Host" href="/host" />{/snippet}
		{#if !live.ready}
			<Skeleton rows={2} kind="bars" />
		{:else}
			<MemoryBars profile={live.host} instances={live.instances.values()} slots={live.slots.values()} />
		{/if}
	</Card>
{/snippet}

{#snippet tasksCard()}
	<Card title="Recent tasks" flush>
		{#snippet tools()}<RowAction label="All tasks" href="/tasks" />{/snippet}
		{#if !live.ready}
			<Skeleton rows={4} dot={false} primary="24ch" secondary={false} menu={false} action={false} />
		{:else if recentTasks.length === 0}
			<Empty text="No tasks yet" hint="Pulls, installs and launches show their progress here" />
		{:else}
			<List>
				{#each recentTasks as t (t.id)}
					<ListRow primary={t.title} primaryKind="text" facts={[{ value: t.finishedAt ? ago(t.finishedAt, clockTick.now) : 'running' }]} onclick={() => goto(`/tasks/${t.id}`)}>
						{#snippet leading()}
							<Mark state={markOf(t)} label={enumLabel(TaskStateSchema, t.state)} tip={t.error || undefined} />
							<span class="kind"><StatePill label={t.kind} /></span>
						{/snippet}
					</ListRow>
				{/each}
			</List>
		{/if}
	</Card>
{/snippet}

<PageHeader>
	{#snippet meta()}
		{#if live.ready}
			{#if attn.length === 0}
				<Dot lamp="success" text="All clear" />
			{:else}
				<Dot lamp={attn.some((a) => a.lamp === 'error') ? 'error' : 'warning'} text="{plural(attn.length, 'item')} need attention" />
			{/if}
		{/if}
	{/snippet}
	{#snippet actions()}
		<NeoButton class="primary" elevation={2} href="/serve/launch">Launch</NeoButton>
	{/snippet}
</PageHeader>

<Card>
	{#if !live.ready}
		<Skeleton rows={1} kind="strip" />
	{:else}
		<Pipeline {stages} />
	{/if}
</Card>

<Columns ratio="3/2">
	{#snippet left()}
		{#if attn.length}{@render attentionCard()}{/if}
		{@render servingCard()}
	{/snippet}
	{#snippet right()}
		{@render memoryCard()}
		{@render tasksCard()}
	{/snippet}
</Columns>

<style>
	.kind {
		display: inline-flex;
		min-width: 9ch;
	}
</style>
