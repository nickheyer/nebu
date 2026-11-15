<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { ms, num, plural } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { RouteState, type Objective, type Route, type SchedulerConfig } from '#lib/proto/nebu/v1/gateway_pb.js';
	import type { Pool } from '#lib/proto/nebu/v1/pool_pb.js';
	import type { PoolImport } from '#lib/proto/nebu/v1/poolimport_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Conditions from '#lib/ui/Conditions.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import type { MenuItem } from '#lib/ui/Menu.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StateWord from '#lib/ui/StateWord.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { routeLamp, routeShape } from '#lib/ui/status.js';
	import GatewayStrip from '#lib/pages/gateway/GatewayStrip.svelte';
	import ObjectiveEditor from '#lib/pages/gateway/ObjectiveEditor.svelte';
	import PoolEditor from '#lib/pages/gateway/PoolEditor.svelte';
	import PoolPanel from '#lib/pages/gateway/PoolPanel.svelte';
	import RouteEditor from '#lib/pages/gateway/RouteEditor.svelte';
	import SchedulerEditor from '#lib/pages/gateway/SchedulerEditor.svelte';
	import { conditionsLamp, endpointState, isUserOwned, ownerLink, poolLamp, poolShape, poolTip, routeTarget, routeTip, schedulerSummary, schedulerWord } from '#lib/pages/gateway/gateway.js';
	import { GatewayGroup } from '#lib/pages/gateway/group.svelte.js';
	import { gatewayHeaderTabs, isGatewayTab, type GatewayTab } from '#lib/pages/gateway/tabs.js';
	import CloudArrowDownIcon from 'phosphor-svelte/lib/CloudArrowDownIcon';
	import FlowArrowIcon from 'phosphor-svelte/lib/FlowArrowIcon';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import SignpostIcon from 'phosphor-svelte/lib/SignpostIcon';
	import StackIcon from 'phosphor-svelte/lib/StackIcon';
	import TargetIcon from 'phosphor-svelte/lib/TargetIcon';

	let { tab = 'routes', id = '' }: { tab?: string; id?: string } = $props();

	const active = $derived<GatewayTab>(isGatewayTab(tab) ? tab : 'routes');
	const group = new GatewayGroup();
	onMount(() => group.start());

	const routes = $derived([...live.routes.values()].sort((a, b) => a.name.localeCompare(b.name)));
	const pools = $derived([...live.pools.values()].sort((a, b) => a.id.localeCompare(b.id)));
	const objectives = $derived([...live.objectives.values()].sort((a, b) => b.priority - a.priority || a.name.localeCompare(b.name)));
	const configs = $derived([...live.schedulerConfigs.values()].sort((a, b) => a.name.localeCompare(b.name)));

	const creating = $derived(id === 'new');
	// Beside an open panel the rows keep to the lamp, the name and the count
	const slim = $derived(id !== '');
	const editing = $derived(page.url.searchParams.get('edit') === '1');
	const route = $derived(active === 'routes' && !creating ? live.routes.get(id) : undefined);
	const pool = $derived(active === 'pools' && !creating ? live.pools.get(id) : undefined);
	const objective = $derived(active === 'objectives' && !creating ? live.objectives.get(id) : undefined);
	const config = $derived(active === 'schedulers' && !creating ? live.schedulerConfigs.get(id) : undefined);
	const adds: Record<Exclude<GatewayTab, 'imports'>, string> = { routes: 'Route', pools: 'Pool', objectives: 'Objective', schedulers: 'Scheduler' };

	function close() {
		void goto(`/gateway/${active}`);
	}

	function hrefOf(t: GatewayTab, name: string): string {
		return `/gateway/${t}/${encodeURIComponent(name)}`;
	}

	async function remove(kind: 'route' | 'pool' | 'objective' | 'scheduler', name: string) {
		try {
			if (kind === 'route') await rpc.gateway.deleteRoute({ name });
			else if (kind === 'pool') await rpc.gateway.deletePool({ id: name });
			else if (kind === 'objective') await rpc.gateway.deleteObjective({ name });
			else await rpc.gateway.deleteSchedulerConfig({ name });
			toast.success(`Deleted ${name}`);
			if (id === name) close();
		} catch (err) {
			toast.error(`Delete ${kind} failed`, reason(err));
		}
	}

	function referencedBy(configName: string): number {
		return pools.filter((p) => p.spec?.schedulerConfig === configName).length;
	}

	function routeMenu(r: Route): MenuItem[] {
		const owner = ownerLink(live, r);
		if (owner) return [{ label: owner.label, href: owner.href }];
		return [{ label: 'Delete', danger: true, confirm: `Delete ${r.name}?`, onclick: () => remove('route', r.name) }];
	}

	function poolMenu(p: Pool): MenuItem[] {
		if (!isUserOwned(p.ownerKind)) return [];
		return [
			{ label: 'Edit', href: `${hrefOf('pools', p.id)}?edit=1` },
			{ label: 'Delete', danger: true, confirm: `Delete ${p.id}?`, onclick: () => remove('pool', p.id) }
		];
	}

	function targetMs(v: number): string {
		return v ? ms(v) : '—';
	}
</script>

{#snippet addButton()}
	{#if active !== 'imports'}
		<NeoButton class="primary" elevation={2} href="/gateway/{active}/new">{#snippet icon()}<PlusIcon size={16} />{/snippet}{adds[active]}</NeoButton>
	{/if}
{/snippet}

{#snippet routeRow(r: Route)}
	{@const state = r.status?.state ?? RouteState.UNSPECIFIED}
	<ListRow
		lamp={routeLamp(state)}
		shape={routeShape(state)}
		primary={r.name}
		primaryTip={routeTip(r)}
		secondary={slim ? undefined : routeTarget(live, r)}
		secondaryWidth="24ch"
		selected={r.name === id}
		onclick={() => goto(hrefOf('routes', r.name))}
		menu={routeMenu(r)}
	/>
{/snippet}

{#snippet poolRow(p: Pool)}
	<ListRow
		lamp={poolLamp(p)}
		shape={poolShape(p)}
		primary={p.id}
		primaryTip={poolTip(p)}
		secondary={slim ? undefined : (p.spec?.model ?? '')}
		secondaryKind="repo"
		secondaryWidth="40ch"
		facts={[{ value: `${num(p.status?.readyEndpoints)} / ${num(p.status?.totalEndpoints)}`, tip: 'Ready of total' }]}
		selected={p.id === id}
		onclick={() => goto(hrefOf('pools', p.id))}
		menu={poolMenu(p)}
	/>
{/snippet}

{#snippet objectiveRow(o: Objective)}
	<ListRow
		primary={o.name}
		facts={[
			{ value: String(o.priority), tip: 'Higher is turned away last under load' },
			{ value: targetMs(o.ttftMs), tip: o.ttftMs ? 'The target' : 'No target' },
			{ value: targetMs(o.tpotMs), tip: o.tpotMs ? 'The target' : 'No target' }
		]}
		selected={o.name === id}
		onclick={() => goto(hrefOf('objectives', o.name))}
		menu={[{ label: 'Delete', danger: true, confirm: `Delete ${o.name}?`, onclick: () => remove('objective', o.name) }]}
	/>
{/snippet}

{#snippet schedulerRow(c: SchedulerConfig)}
	{@const refs = referencedBy(c.name)}
	<ListRow
		lamp={conditionsLamp(c.status?.conditions)}
		primary={c.name}
		secondary={slim ? undefined : schedulerSummary(c.spec?.yaml ?? '') || '\u2014'}
		secondaryWidth="32ch"
		facts={[{ value: schedulerWord(c.status?.conditions) }, { value: refs ? plural(refs, 'pool') : 'no pool' }]}
		selected={c.name === id}
		onclick={() => goto(hrefOf('schedulers', c.name))}
		menu={[{ label: 'Delete', danger: true, disabled: refs > 0, description: refs > 0 ? `used by ${plural(refs, 'pool')}` : undefined, confirm: `Delete ${c.name}?`, onclick: () => remove('scheduler', c.name) }]}
	/>
{/snippet}

{#snippet importRow(imp: PoolImport)}
	{@const eps = imp.status?.endpoints ?? []}
	<ListRow primary={imp.id} facts={[{ value: `${num(eps.filter((e) => e.ready).length)} / ${num(eps.length)}`, tip: 'Ready of total' }]}>
		{#snippet badges()}<StateWord label={imp.status?.routingMode || imp.spec?.routingMode || 'endpoint'} lamp="primary" />{/snippet}
		{#snippet expand()}
			<Stack gap={16}>
				<Section title="Controllers" level={3}>
					{#each imp.status?.controllers ?? [] as c (c.name)}
						<ListRow
							lamp={conditionsLamp(c.conditions)}
							primary={c.name}
							secondary={c.exportingSources.map((s) => live.nodes.get(s.nodeId)?.name ?? s.nodeId).join(', ')}
							secondaryTip={c.exportingSources.map((s) => `${live.nodes.get(s.nodeId)?.name ?? s.nodeId} mesh ${s.meshHash}`).join('\n') || undefined}
							secondaryWidth="24ch"
						>
							{#snippet expand()}<Conditions conditions={c.conditions} />{/snippet}
						</ListRow>
					{:else}
						<Empty text="No controllers" row />
					{/each}
				</Section>
				<Section title="Endpoints" level={3}>
					{#each eps as e (e.id)}
						{@const state = endpointState(e)}
						<ListRow lamp={state.lamp} shape={state.shape} primary={e.address} primaryKind="address" secondary={live.nodes.get(e.nodeId)?.name ?? e.nodeId} secondaryWidth="24ch" facts={[{ value: state.word }]} />
					{:else}
						<Empty text="No endpoints" row />
					{/each}
				</Section>
			</Stack>
		{/snippet}
	</ListRow>
{/snippet}

{#snippet listCard()}
	{#if active === 'routes'}
		<Card title="Routes" tools={addButton} flush>
			{#if !live.ready}
				<Skeleton rows={4} secondary="24ch" />
			{:else if routes.length === 0}
				<Empty icon={SignpostIcon} text="No routes" hint="A route is a model name clients ask for, sent to one pool or split over several" />
			{:else}
				<List head={{ primary: 'Route', secondary: 'Target' }}>
					{#each routes as r (r.name)}{@render routeRow(r)}{/each}
				</List>
			{/if}
		</Card>
	{:else if active === 'pools'}
		<Card title="Pools" tools={addButton} flush>
			{#if !live.ready}
				<Skeleton rows={4} secondary="40ch" />
			{:else if pools.length === 0}
				<Empty icon={StackIcon} text="No pools" hint="A pool is the set of endpoints serving one model, and a launch makes its own" />
			{:else}
				<List head={{ primary: 'Pool', secondary: 'Model', facts: ['Ready'] }}>
					{#each pools as p (p.id)}{@render poolRow(p)}{/each}
				</List>
			{/if}
		</Card>
	{:else if active === 'objectives'}
		<Card title="Objectives" tools={addButton} flush>
			{#if !live.ready}
				<Skeleton rows={3} dot={false} secondary="16ch" facts={2} action={false} />
			{:else if objectives.length === 0}
				<Empty icon={TargetIcon} text="No objectives" hint="An objective gives the requests of a route a priority and latency targets" />
			{:else}
				<List head={{ primary: 'Objective', facts: ['Priority', 'Time to first token', 'Time per output token'] }}>
					{#each objectives as o (o.name)}{@render objectiveRow(o)}{/each}
				</List>
			{/if}
		</Card>
	{:else}
		<Card title="Schedulers" tools={addButton} flush>
			{#if !live.ready}
				<Skeleton rows={3} secondary="14ch" action={false} />
			{:else if configs.length === 0}
				<Empty icon={FlowArrowIcon} text="No schedulers" hint="A scheduler picks the endpoint of a pool each request goes to" />
			{:else}
				<List head={{ primary: 'Scheduler', secondary: 'Holds', facts: ['State', 'Used by'] }}>
					{#each configs as c (c.name)}{@render schedulerRow(c)}{/each}
				</List>
			{/if}
		</Card>
	{/if}
{/snippet}

{#snippet missing(text: string)}
	<PanelHeader title={id} onclose={close} />
	{#if live.ready}
		<Empty {text} action="Back" onclick={close} />
	{:else}
		<Skeleton rows={4} kind="facts" />
	{/if}
{/snippet}

{#snippet strip()}
	<GatewayStrip status={group.status} />
{/snippet}

<PageHeader tabs={gatewayHeaderTabs} {active} />

{#if active === 'imports'}
	{@render strip()}
	<Card title="Imports" flush>
		{#if !group.importsLoaded}
			<Skeleton rows={3} dot={false} secondary={false} action={false} menu={false} />
		{:else if group.imports.length === 0}
			<Empty icon={CloudArrowDownIcon} text="No imported pools" hint="Pools the other nodes of a mesh export are listed here" action={live.inMesh ? undefined : 'Set up a mesh'} href="/mesh" />
		{:else}
			<List head={{ primary: 'Pool', facts: ['Ready'] }}>
				{#each group.imports as imp (imp.id)}{@render importRow(imp)}{/each}
			</List>
		{/if}
	</Card>
{:else}
	<Sheet open={id !== ''} onclose={close} head={strip} list={listCard}>
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
			{:else if creating || config}
				<SchedulerEditor {config} references={config ? referencedBy(config.name) : 0} onclose={close} />
			{:else}
				{@render missing('No such scheduler')}
			{/if}
		{/key}
	</Sheet>
{/if}
