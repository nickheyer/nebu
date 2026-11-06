<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { isCancel, reason, rpc } from '#lib/api/transport.js';
	import { enumLabel, ms, num, plural } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { RouteState, RouteStateSchema, type GatewayStatus } from '#lib/proto/nebu/v1/gateway_pb.js';
	import type { PoolImport } from '#lib/proto/nebu/v1/poolimport_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Conditions from '#lib/ui/Conditions.svelte';
	import Dot from '#lib/ui/Dot.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import Facts, { type FactItem } from '#lib/ui/Facts.svelte';
	import InfoTip from '#lib/ui/InfoTip.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { routeLamp, routeShape } from '#lib/ui/status.js';
	import ObjectiveEditor from '#lib/pages/gateway/ObjectiveEditor.svelte';
	import PoolEditor from '#lib/pages/gateway/PoolEditor.svelte';
	import PoolPanel from '#lib/pages/gateway/PoolPanel.svelte';
	import RouteEditor from '#lib/pages/gateway/RouteEditor.svelte';
	import SchedulerEditor from '#lib/pages/gateway/SchedulerEditor.svelte';
	import {
		balanceWord,
		compact,
		conditionsLamp,
		isGatewayTab,
		isUserOwned,
		poolLamp,
		poolShape,
		poolTip,
		poolWord,
		priorityBand,
		routeAction,
		routeTarget,
		routeTip,
		shortAddr,
		statusFacts,
		targetShare,
		type GatewayTab
	} from '#lib/pages/gateway/gateway.js';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';

	let { tab = 'routes', id = '' }: { tab?: string; id?: string } = $props();

	const active = $derived<GatewayTab>(isGatewayTab(tab) ? tab : 'routes');
	let status = $state<GatewayStatus | undefined>(undefined);
	let imports = $state<PoolImport[]>([]);
	let importsLoaded = $state(false);

	async function loadStatus() {
		try {
			status = (await rpc.gateway.getGatewayStatus({})).status;
		} catch (err) {
			toast.error('Gateway status failed to load', reason(err));
		}
	}

	async function loadImports() {
		try {
			imports = (await rpc.imports.listPoolImports({})).imports;
		} catch (err) {
			toast.error('Imported pools failed to load', reason(err));
		} finally {
			importsLoaded = true;
		}
	}

	// Follows imported pools, reconnecting with backoff and toasting once per run of failures
	async function watchImports(signal: AbortSignal) {
		let delay = 1000;
		let warned = false;
		while (!signal.aborted) {
			try {
				for await (const w of rpc.imports.watchPoolImports({}, { signal })) {
					delay = 1000;
					warned = false;
					const imp = w.import;
					if (!imp) continue;
					imports = w.deleted ? imports.filter((x) => x.id !== imp.id) : [...imports.filter((x) => x.id !== imp.id), imp].sort((a, b) => a.id.localeCompare(b.id));
				}
			} catch (err) {
				if (isCancel(err) || signal.aborted) return;
				if (!warned) toast.error('Imported pools stopped updating', reason(err));
				warned = true;
			}
			await new Promise((resolve) => setTimeout(resolve, delay));
			delay = Math.min(delay * 2, 30_000);
			if (!signal.aborted) await loadImports();
		}
	}

	onMount(() => {
		void loadStatus();
		void loadImports();
		const abort = new AbortController();
		void watchImports(abort.signal);
		let timer: ReturnType<typeof setTimeout> | undefined;
		const off = live.on(EventKind.ROUTE, () => {
			clearTimeout(timer);
			timer = setTimeout(() => void loadStatus(), 2000);
		});
		return () => {
			abort.abort();
			clearTimeout(timer);
			off();
		};
	});

	const routes = $derived([...live.routes.values()].sort((a, b) => a.name.localeCompare(b.name)));
	const pools = $derived([...live.pools.values()].sort((a, b) => a.id.localeCompare(b.id)));
	const objectives = $derived([...live.objectives.values()].sort((a, b) => b.priority - a.priority || a.name.localeCompare(b.name)));
	const configs = $derived([...live.schedulerConfigs.values()].sort((a, b) => a.name.localeCompare(b.name)));
	const tabs = $derived([
		{ id: 'routes', label: 'Routes', count: routes.length },
		{ id: 'pools', label: 'Pools', count: pools.length },
		{ id: 'objectives', label: 'Objectives', count: objectives.length },
		{ id: 'schedulers', label: 'Schedulers', count: configs.length },
		{ id: 'imports', label: 'Imports', count: imports.length }
	]);

	const facts = $derived.by((): FactItem[] => {
		if (!status) return [];
		const first = status.listeners[0];
		return [
			{ id: 'version', label: status.implementation || 'nebu', value: status.version || 'dev' },
			{ id: 'listener', label: 'Listener', value: `${first ? shortAddr(first.addr) : 'none'}${status.tls ? ' TLS' : ''}`, tip: status.listeners.map((l) => l.addr).join('\n') || undefined },
			{ id: 'auth', label: 'Auth', value: status.auth ? 'on' : 'off', lamp: status.auth ? undefined : 'warning' },
			{ id: 'requests', label: 'Requests', value: compact(status.requests), tip: num(status.requests), href: '/traffic' }
		];
	});
	const info = $derived(status ? statusFacts(status) : []);

	const creating = $derived(id === 'new');
	const editing = $derived(page.url.searchParams.get('edit') === '1');
	const route = $derived(active === 'routes' && !creating ? live.routes.get(id) : undefined);
	const pool = $derived(active === 'pools' && !creating ? live.pools.get(id) : undefined);
	const objective = $derived(active === 'objectives' && !creating ? live.objectives.get(id) : undefined);
	const config = $derived(active === 'schedulers' && !creating ? live.schedulerConfigs.get(id) : undefined);
	const panelOpen = $derived(id !== '');
	const adds: Record<GatewayTab, string> = { routes: 'Route', pools: 'Pool', objectives: 'Objective', schedulers: 'Scheduler', imports: '' };
	const empties: Record<GatewayTab, string> = { routes: 'Select a route', pools: 'Select a pool', objectives: 'Select an objective', schedulers: 'Select a scheduler', imports: '' };

	function close() {
		void goto(`/gateway/${active}`);
	}

	function hrefOf(t: GatewayTab, name: string): string {
		return `/gateway/${t}/${encodeURIComponent(name)}`;
	}

	async function remove(kind: 'route' | 'pool' | 'objective', name: string) {
		try {
			if (kind === 'route') await rpc.gateway.deleteRoute({ name });
			else if (kind === 'pool') await rpc.gateway.deletePool({ id: name });
			else await rpc.gateway.deleteObjective({ name });
			toast.success(`Deleted ${name}`);
			if (id === name) close();
		} catch (err) {
			toast.error(`Delete ${kind} failed`, reason(err));
		}
	}

	function referencedBy(configName: string): number {
		return pools.filter((p) => p.spec?.schedulerConfig === configName).length;
	}
</script>

{#snippet header()}
	{#if status}
		<Facts items={facts} size="section">
			{#snippet after()}<InfoTip click facts={info} />{/snippet}
		</Facts>
	{:else}
		<Skeleton rows={1} height={36} />
	{/if}
	<PageTabs {tabs} {active} base="/gateway" />
{/snippet}

{#snippet toolbar()}
	<Row gap={12}>
		{#if active === 'objectives'}
			<Cell grow={false}><InfoTip text="Chosen by the request header or inherited from the route" /></Cell>
		{/if}
		<Cell grow={false}>
			<NeoButton elevation={2} href="/gateway/{active}/new">{#snippet icon()}<PlusIcon size={20} />{/snippet}{adds[active]}</NeoButton>
		</Cell>
	</Row>
{/snippet}

{#snippet routeRows()}
	{#if routes.length === 0}
		<Empty text="No routes" action="Launch" href="/serve/launch" />
	{/if}
	<Stack gap={0}>
		{#each routes as r (r.name)}
			{@const state = r.status?.state ?? RouteState.UNSPECIFIED}
			{@const act = routeAction(live, r)}
			<ListRow
				lamp={routeLamp(state)}
				shape={routeShape(state)}
				primary={r.name}
				primaryTip={routeTip(r)}
				secondary={routeTarget(live, r)}
				secondaryWidth="24ch"
				facts={[{ value: state === RouteState.READY ? `${num(r.status?.inFlight)} in flight` : enumLabel(RouteStateSchema, state) }]}
				selected={r.name === id}
				onclick={() => goto(hrefOf('routes', r.name))}
				menu={isUserOwned(r.ownerKind) ? [{ label: 'Delete', danger: true, confirm: `Delete ${r.name}`, onclick: () => remove('route', r.name) }] : undefined}
			>
				{#snippet action()}<NeoButton text href={act.href}>{act.label}</NeoButton>{/snippet}
				{#snippet expand()}
					<Stack gap={12}>
						<Stack gap={4}>
							{#each r.spec?.rules ?? [] as rule, i (i)}
								<Str value={rule.match || 'every request'} kind="address" weight={500} />
								{#each rule.targets as t, j (j)}
									{@const target = live.pools.get(t.poolId)}
									<div class="indent">
										<Row gap={12} wrap={false}>
											<Dot lamp={poolLamp(target)} shape={poolShape(target)} />
											<Cell min="8ch" max="24ch"><Str value={t.poolId} width="100%" /></Cell>
											<Cell grow={false}><span class="num">{targetShare(rule, t.weight)}</span></Cell>
											<Cell min="6ch" max="24ch"><Str value={t.model ? `as ${t.model}` : 'pool model'} color="muted" width="100%" /></Cell>
											<Cell grow={false}><span class="secondary nowrap">{poolWord(target)}</span></Cell>
										</Row>
									</div>
								{/each}
							{:else}
								<span class="secondary">No rules</span>
							{/each}
						</Stack>
						<KeyRows
							dense
							rows={[
								{ label: 'objective', value: r.spec?.objective || 'none', href: r.spec?.objective ? hrefOf('objectives', r.spec.objective) : undefined },
								{ label: 'balance', value: balanceWord(r.spec?.balance) },
								{ label: 'tools', value: r.spec?.profile?.toolServers.join(', ') || 'none', tip: r.spec?.profile?.toolServers.join('\n') || undefined }
							]}
						/>
						<Conditions conditions={r.status?.conditions} />
					</Stack>
				{/snippet}
			</ListRow>
		{/each}
	</Stack>
{/snippet}

{#snippet poolRows()}
	{#if pools.length === 0}
		<Empty text="No pools" action="Pool" href="/gateway/pools/new" />
	{/if}
	<Stack gap={0}>
		{#each pools as p (p.id)}
			{@const href = hrefOf('pools', p.id)}
			<ListRow
				lamp={poolLamp(p)}
				shape={poolShape(p)}
				primary={p.id}
				primaryTip={poolTip(p)}
				secondary={p.spec?.model ?? ''}
				secondaryKind="repo"
				secondaryWidth="40ch"
				facts={[{ value: poolWord(p) }]}
				selected={p.id === id}
				onclick={() => goto(href)}
				menu={isUserOwned(p.ownerKind) ? [{ label: 'Edit', href: `${href}?edit=1` }, { label: 'Delete', danger: true, confirm: `Delete ${p.id}`, onclick: () => remove('pool', p.id) }] : undefined}
			>
				{#snippet action()}<NeoButton text {href}>Open</NeoButton>{/snippet}
			</ListRow>
		{/each}
	</Stack>
{/snippet}

{#snippet objectiveRows()}
	{#if objectives.length === 0}
		<Empty text="No objectives" action="Objective" href="/gateway/objectives/new" />
	{/if}
	<Stack gap={0}>
		{#each objectives as o (o.name)}
			{@const href = hrefOf('objectives', o.name)}
			<ListRow
				primary={o.name}
				secondary="{o.priority} {priorityBand(o.priority)}"
				secondaryWidth="16ch"
				facts={[
					{ value: `TTFT ${o.ttftMs ? ms(o.ttftMs) : 'none'}`, tip: 'Time to first token' },
					{ value: `TPOT ${o.tpotMs ? ms(o.tpotMs) : 'none'}`, tip: 'Time per output token' }
				]}
				selected={o.name === id}
				onclick={() => goto(href)}
				menu={[
					{ label: 'Edit', href },
					{ label: 'Delete', danger: true, confirm: `Delete ${o.name}`, onclick: () => remove('objective', o.name) }
				]}
			/>
		{/each}
	</Stack>
{/snippet}

{#snippet schedulerRows()}
	{#if configs.length === 0}
		<Empty text="No schedulers" action="Scheduler" href="/gateway/schedulers/new" />
	{/if}
	<Stack gap={0}>
		{#each configs as c (c.name)}
			{@const refs = referencedBy(c.name)}
			{@const href = hrefOf('schedulers', c.name)}
			<ListRow
				lamp={conditionsLamp(c.status?.conditions)}
				primary={c.name}
				facts={[{ value: plural(refs, 'pool'), tip: `Referenced by ${plural(refs, 'pool')}` }]}
				selected={c.name === id}
				onclick={() => goto(href)}
			>
				{#snippet action()}<NeoButton text {href}>Open</NeoButton>{/snippet}
			</ListRow>
		{/each}
	</Stack>
{/snippet}

{#snippet importRows()}
	{#if !importsLoaded}
		<Skeleton rows={3} />
	{:else if imports.length === 0}
		<Empty text="No imported pools" action="Mesh" href="/mesh" />
	{/if}
	<Stack gap={0}>
		{#each imports as imp (imp.id)}
			{@const eps = imp.status?.endpoints ?? []}
			<ListRow primary={imp.id} facts={[{ value: `${num(eps.filter((e) => e.ready).length)} / ${num(eps.length)} ready` }]}>
				{#snippet badges()}<StatePill label={imp.status?.routingMode || imp.spec?.routingMode || 'endpoint'} lamp="primary" />{/snippet}
				{#snippet expand()}
					<Stack gap={8}>
						<span class="label">Controllers</span>
						<Stack gap={0}>
							{#each imp.status?.controllers ?? [] as c (c.name)}
								<ListRow
									lamp={conditionsLamp(c.conditions)}
									primary={c.name}
									secondary={c.exportingSources.map((s) => live.nodes.get(s.nodeId)?.name ?? s.nodeId).join(', ') || 'no sources'}
									secondaryTip={c.exportingSources.map((s) => `${live.nodes.get(s.nodeId)?.name ?? s.nodeId} mesh ${s.meshHash}`).join('\n') || undefined}
									secondaryWidth="24ch"
								>
									{#snippet expand()}<Conditions conditions={c.conditions} />{/snippet}
								</ListRow>
							{:else}
								<span class="secondary">No controllers</span>
							{/each}
						</Stack>
						<span class="label">Endpoints</span>
						<Stack gap={0}>
							{#each eps as e (e.id)}
								<ListRow primary={e.address} primaryKind="address" secondary={live.nodes.get(e.nodeId)?.name ?? e.nodeId} secondaryWidth="24ch">
									{#snippet leading()}
										<Tip text={e.ready ? 'Ready' : 'Not ready'} tag="span"><Dot lamp={e.ready ? 'success' : 'muted'} shape={e.ready ? 'filled' : 'hollow'} label="ready" /></Tip>
										<Tip text={e.healthy ? 'Healthy' : 'Unhealthy'} tag="span"><Dot lamp={e.healthy ? 'success' : 'error'} label="healthy" /></Tip>
									{/snippet}
								</ListRow>
							{:else}
								<span class="secondary">No endpoints</span>
							{/each}
						</Stack>
					</Stack>
				{/snippet}
			</ListRow>
		{/each}
	</Stack>
{/snippet}

{#snippet list()}
	<Stack gap={12}>
		{@render toolbar()}
		{#if !live.ready}
			<Skeleton rows={4} />
		{:else if active === 'routes'}
			{@render routeRows()}
		{:else if active === 'pools'}
			{@render poolRows()}
		{:else if active === 'objectives'}
			{@render objectiveRows()}
		{:else}
			{@render schedulerRows()}
		{/if}
	</Stack>
{/snippet}

{#snippet missing(text: string)}
	{#if live.ready}
		<Empty {text} action="Back" onclick={close} />
	{:else}
		<Skeleton rows={4} height={32} />
	{/if}
{/snippet}

<Stack gap={16}>
	{@render header()}
	{#if active === 'imports'}
		{@render importRows()}
	{:else}
		<Sheet open={panelOpen} onclose={close} {list} empty={empties[active]} emptyAction={adds[active]} emptyHref="/gateway/{active}/new">
			{#key `${active}/${id}/${editing}`}
				{#if active === 'routes'}
					{#if creating || route}
						<RouteEditor {route} onclose={close} />
					{:else}
						{@render missing('No such route')}
					{/if}
				{:else if active === 'pools'}
					{#if creating || (pool && editing && isUserOwned(pool.ownerKind))}
						<PoolEditor {pool} onclose={close} />
					{:else if pool}
						<PoolPanel {pool} onclose={close} />
					{:else}
						{@render missing('No such pool')}
					{/if}
				{:else if active === 'objectives'}
					{#if creating || objective}
						<ObjectiveEditor {objective} onclose={close} />
					{:else}
						{@render missing('No such objective')}
					{/if}
				{:else if active === 'schedulers'}
					{#if creating || config}
						<SchedulerEditor {config} references={config ? referencedBy(config.name) : 0} onclose={close} />
					{:else}
						{@render missing('No such scheduler')}
					{/if}
				{/if}
			{/key}
		</Sheet>
	{/if}
</Stack>

<style>
	.indent {
		display: flex;
		min-width: 0;
		padding-left: 16px;
	}
</style>
