<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoSortableItem, NeoSortableProvider } from '#lib/neo/sortable/index.js';
	import type { NeoSortableItem as SortItem } from '#lib/neo/sortable/neo-sortable-context.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { date, enumLabel, num, plural } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { ShapeSchema } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { InstanceState } from '#lib/proto/nebu/v1/instance_pb.js';
	import type { Slot } from '#lib/proto/nebu/v1/slot_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { prefs } from '#lib/state/prefs.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import MemoryBars from '#lib/ui/MemoryBars.svelte';
	import NameConfirm from '#lib/ui/NameConfirm.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import Columns from '#lib/ui/layout/Columns.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import { compactBelow } from '#lib/ui/list.svelte.js';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { formationLamp } from '#lib/ui/status.js';
	import InstancePanel from '#lib/pages/serve/InstancePanel.svelte';
	import InstanceRow from '#lib/pages/serve/InstanceRow.svelte';
	import LaunchWizard, { type WizardStart } from '#lib/pages/serve/LaunchWizard.svelte';
	import SlotEditor from '#lib/pages/serve/SlotEditor.svelte';
	import SlotRow from '#lib/pages/serve/SlotRow.svelte';
	import { slotHref, slotUpdate, splitKey, terminal } from '#lib/pages/serve/serve.js';
	import { serveTabs } from '#lib/pages/serve/tabs.js';
	import PlayIcon from 'phosphor-svelte/lib/PlayIcon';

	let { instanceId }: { instanceId: string } = $props();

	const launching = $derived(instanceId === 'launch');
	const selected = $derived(launching ? undefined : live.instances.get(instanceId));
	// The slot the panel edits, new for the create form, read from the query so a reload keeps it
	const slotParam = $derived(instanceId === '' ? page.url.searchParams.get('slot') : null);
	const editing = $derived(slotParam && slotParam !== 'new' ? live.slots.get(slotParam) : undefined);
	const panelOpen = $derived(slotParam !== null || (!launching && instanceId !== ''));
	const tab = $derived(page.url.searchParams.get('tab') ?? '');
	let listWidth = $state(0);
	// Beside an open panel the rows keep the lamp, name and action, the panel holding the rest
	const slim = $derived(panelOpen && listWidth > 0 && listWidth < compactBelow);

	const slots = $derived([...live.slots.values()].sort((a, b) => a.position - b.position || a.name.localeCompare(b.name)));
	let sortItems = $state<SortItem<{ id: string }>[]>([]);
	$effect(() => {
		sortItems = slots.map((s) => ({ id: s.id, data: { id: s.id } }));
	});
	const slotById = $derived(new Map(slots.map((s) => [s.id, s])));

	const dismissed = $derived(new Set(prefs.serveDismissed));
	// Loose instances, the stopped and failed ones kept a day until dismissed
	const loose = $derived(
		[...live.instances.values()]
			.filter((i) => i.slotId === '' && !dismissed.has(i.id))
			.filter((i) => !terminal(i.state) || clockTick.now - (date(i.stoppedAt ?? i.createdAt)?.getTime() ?? 0) < 86_400_000)
			.sort((a, b) => Number(terminal(a.state)) - Number(terminal(b.state)) || a.name.localeCompare(b.name))
	);
	const formations = $derived(live.inMesh ? [...live.formations.values()].sort((a, b) => a.name.localeCompare(b.name)) : []);
	const nothing = $derived(slots.length === 0 && loose.length === 0 && formations.length === 0);
	const running = $derived([...live.instances.values()].filter((i) => i.state === InstanceState.READY || i.state === InstanceState.IDLE || i.state === InstanceState.DRAINING).length);
	const failed = $derived([...live.instances.values()].filter((i) => i.state === InstanceState.FAILED && !dismissed.has(i.id)).length);

	// Forgets dismissed ids the daemon no longer holds
	$effect(() => {
		if (!live.ready) return;
		const kept = prefs.serveDismissed.filter((id) => live.instances.has(id));
		if (kept.length !== prefs.serveDismissed.length) prefs.serveDismissed = kept;
	});

	let deleting = $state<Slot | undefined>(undefined);
	let deleteOpen = $state(false);

	const wizardStart = $derived.by((): WizardStart => {
		const q = page.url.searchParams;
		const swap = q.get('swap');
		const model = q.get('model');
		const step = q.get('step');
		const context = q.get('context');
		return {
			slotId: swap ?? q.get('slot') ?? undefined,
			swap: swap !== null,
			model: model ? splitKey(model) : undefined,
			runtimeId: q.get('runtime') ?? undefined,
			params: context ? { n_ctx: context } : undefined,
			step: step === null ? undefined : Number(step) || 0,
			plan: q.get('plan') !== null
		};
	});

	function close() {
		void goto('/serve');
	}

	function askDelete(slot: Slot) {
		deleting = slot;
		deleteOpen = true;
	}

	async function deleteSlot() {
		if (!deleting) return;
		try {
			await rpc.slots.deleteSlot({ id: deleting.id, force: deleting.instanceId !== '' });
			toast.success(`Deleted ${deleting.name}`);
			if (slotParam === deleting.id) close();
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}

	async function reordered() {
		for (const [i, item] of sortItems.entries()) {
			const slot = slotById.get(String(item.id));
			if (!slot || slot.position === i + 1) continue;
			try {
				await rpc.slots.updateSlot(slotUpdate(slot, { position: i + 1 }));
			} catch (err) {
				toast.error('Reorder failed', reason(err));
				return;
			}
		}
	}

	function dismiss(id: string) {
		prefs.serveDismissed = [...prefs.serveDismissed, id];
	}

	function showTab(id: string) {
		void goto(`/serve/${encodeURIComponent(instanceId)}?tab=${id}`, { replace: true, reset: false });
	}

	function nodeCount(seats: { nodeId: string }[]): number {
		return new Set(seats.map((s) => s.nodeId)).size;
	}
</script>

{#snippet slotsCard()}
	<Card title="Slots" count={slots.length} info="Drag a slot to set the order slots relaunch in" flush>
		<List head={{ primary: 'Slot', secondary: 'Occupant', facts: ['In flight'] }}>
			<NeoSortableProvider bind:items={sortItems} axis="y" onDragEnd={() => void reordered()}>
				{#each sortItems as item, index (item.id)}
					{@const slot = slotById.get(String(item.id))}
					{#if slot}
						<NeoSortableItem id={item.id} {index} data={item.data}>
							{#snippet children({ instance })}
								<div class="sortrow" {@attach instance.attach}>
									<SlotRow {slot} {slim} handle={instance.attachHandle} ondelete={askDelete} selected={slotParam === slot.id || (slot.instanceId !== '' && slot.instanceId === instanceId)} />
								</div>
							{/snippet}
						</NeoSortableItem>
					{/if}
				{/each}
			</NeoSortableProvider>
		</List>
	</Card>
{/snippet}

{#snippet looseCard()}
	<Card title="Instances outside a slot" count={loose.length} info="Stopped and failed ones stay a day until dismissed" flush>
		<List head={{ primary: 'Instance', secondary: 'Model', facts: ['In flight'] }}>
			{#each loose as inst (inst.id)}
				<InstanceRow instance={inst} {slim} ondismiss={dismiss} selected={inst.id === instanceId} />
			{/each}
		</List>
	</Card>
{/snippet}

{#snippet formationsCard()}
	<Card title="Formations" count={formations.length} flush>
		<List head={{ primary: 'Formation', secondary: 'Shape', facts: ['Nodes'] }}>
			{#each formations as f (f.id)}
				<ListRow
					lamp={formationLamp(f.state)}
					primary={f.name}
					secondary={slim ? undefined : enumLabel(ShapeSchema, f.shape)}
					secondaryWidth="16ch"
					facts={slim ? [] : [{ value: num(nodeCount(f.seats)) }]}
					onclick={() => goto(`/mesh/formations/${encodeURIComponent(f.id)}`)}
				/>
			{/each}
		</List>
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

{#snippet list()}
	<div class="lists" bind:clientWidth={listWidth}>
		<Columns ratio="3/2">
			{#snippet left()}
				{#if !live.ready}
					<Card title="Slots" flush>
						<Skeleton rows={3} leading="20px" secondary="40ch" />
					</Card>
				{:else if nothing}
					<Card>
						<Empty icon={PlayIcon} text="Nothing serving" hint="Launched models run here, each in a slot keeping its memory, runtime and route settings" action="Launch" href="/serve/launch" />
					</Card>
				{:else}
					{#if slots.length}{@render slotsCard()}{/if}
					{#if loose.length}{@render looseCard()}{/if}
					{#if formations.length}{@render formationsCard()}{/if}
				{/if}
			{/snippet}
			{#snippet right()}
				{@render memoryCard()}
			{/snippet}
		</Columns>
	</div>
{/snippet}

<PageHeader tabs={serveTabs} active="instances">
	{#snippet meta()}
		{#if live.ready}
			<span>{running ? `${num(running)} running` : 'Nothing running'}</span>
			{#if slots.length}<span>{plural(slots.length, 'slot')}</span>{/if}
			{#if failed}<span class="error-text">{num(failed)} failed</span>{/if}
		{/if}
	{/snippet}
	{#snippet actions()}
		<NeoButton elevation={2} href={slotHref('new')}>New slot</NeoButton>
		<NeoButton class="primary" elevation={2} href="/serve/launch">Launch</NeoButton>
	{/snippet}
</PageHeader>

<Sheet open={panelOpen} onclose={close} {list}>
	{#if slotParam === 'new'}
		<SlotEditor onclose={close} />
	{:else if slotParam !== null}
		{#if editing}
			{#key editing.id}
				<SlotEditor slot={editing} onclose={close} />
			{/key}
		{:else if live.ready}
			<Empty text="No such slot" action="Back" href="/serve" />
		{:else}
			<Skeleton rows={6} kind="facts" />
		{/if}
	{:else if selected}
		{#key selected.id}
			<InstancePanel instance={selected} {tab} ontab={showTab} onclose={close} />
		{/key}
	{:else if live.ready}
		<Empty text="No such instance" action="Back" href="/serve" />
	{:else}
		<Skeleton rows={6} kind="facts" />
	{/if}
</Sheet>

{#if deleting}
	<NameConfirm bind:open={deleteOpen} title="Delete slot" name={deleting.name} message={deleting.instanceId ? 'The occupant stops first' : undefined} onconfirm={deleteSlot} />
{/if}

{#if launching}
	<LaunchWizard start={wizardStart} />
{/if}

<style>
	.lists {
		min-width: 0;
	}
	/* A drag wrapper passes the list's columns through to its row and keeps the hairline between rows */
	.sortrow {
		display: grid;
		grid-template-columns: subgrid;
		grid-column: 1 / -1;
	}
	.sortrow + .sortrow {
		border-top: 1px solid var(--nebu-border);
	}
</style>
