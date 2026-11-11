<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoSortableItem, NeoSortableProvider } from '#lib/neo/sortable/index.js';
	import type { NeoSortableItem as SortItem } from '#lib/neo/sortable/neo-sortable-context.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { date, enumLabel, num } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { ShapeSchema } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { InstanceState, type Instance } from '#lib/proto/nebu/v1/instance_pb.js';
	import { SlotState, type Slot } from '#lib/proto/nebu/v1/slot_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { prefs } from '#lib/state/prefs.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import MemoryBars from '#lib/ui/MemoryBars.svelte';
	import NameConfirm from '#lib/ui/NameConfirm.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import MultiSelect from '#lib/ui/fields/MultiSelect.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { formationLamp } from '#lib/ui/status.js';
	import InstancePanel from '#lib/pages/serve/InstancePanel.svelte';
	import InstanceRow from '#lib/pages/serve/InstanceRow.svelte';
	import Launch, { type LaunchStart } from '#lib/pages/serve/Launch.svelte';
	import SlotEditor from '#lib/pages/serve/SlotEditor.svelte';
	import SlotRow from '#lib/pages/serve/SlotRow.svelte';
	import { slotHref, slotUpdate, splitKey, terminal } from '#lib/pages/serve/serve.js';
	import { serveTabs } from '#lib/pages/serve/tabs.js';
	import PlayIcon from 'phosphor-svelte/lib/PlayIcon';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';

	let { instanceId }: { instanceId: string } = $props();

	const launching = $derived(instanceId === 'launch');
	const selected = $derived(launching ? undefined : live.instances.get(instanceId));
	// The slot the panel edits, new for the create form
	const slotParam = $derived(instanceId === '' ? page.url.searchParams.get('slot') : null);
	const editing = $derived(slotParam && slotParam !== 'new' ? live.slots.get(slotParam) : undefined);
	const panelOpen = $derived(slotParam !== null || (!launching && instanceId !== ''));
	const tab = $derived(page.url.searchParams.get('tab') ?? '');
	// Beside an open panel the rows keep to the lamp, the name and the action
	const slim = $derived(panelOpen);

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

	type Phase = 'running' | 'failed' | 'pending';
	type Kind = 'slot' | 'standalone';
	type Filter = Phase | Kind;
	const filterOptions: { value: Filter; label: string }[] = [
		{ value: 'running', label: 'Running' },
		{ value: 'failed', label: 'Failed' },
		{ value: 'pending', label: 'Pending' },
		{ value: 'slot', label: 'Slot' },
		{ value: 'standalone', label: 'Standalone' }
	];
	let filters = $state<Filter[]>([]);

	function slotPhase(s: Slot): Phase | undefined {
		if (s.state === SlotState.READY || s.state === SlotState.SWAPPING || s.state === SlotState.DRAINING) return 'running';
		if (s.state === SlotState.STARTING) return 'pending';
		return s.state === SlotState.FAILED ? 'failed' : undefined;
	}

	function instancePhase(i: Instance): Phase | undefined {
		if (i.state === InstanceState.STARTING) return 'pending';
		if (i.state === InstanceState.FAILED) return 'failed';
		return i.state === InstanceState.STOPPED ? undefined : 'running';
	}

	// Whether a row passes the chosen states and the chosen kinds, either left open when none of its words is chosen
	function passes(kind: Kind, phase: Phase | undefined): boolean {
		const kinds = filters.filter((f) => f === 'slot' || f === 'standalone');
		const phases = filters.filter((f) => f !== 'slot' && f !== 'standalone');
		return (kinds.length === 0 || kinds.includes(kind)) && (phases.length === 0 || (phase !== undefined && phases.includes(phase)));
	}

	const shownSlots = $derived(new Set(slots.filter((s) => passes('slot', slotPhase(s))).map((s) => s.id)));
	const shownLoose = $derived(loose.filter((i) => passes('standalone', instancePhase(i))));

	// Forgets dismissed ids the daemon no longer holds
	$effect(() => {
		if (!live.ready) return;
		const kept = prefs.serveDismissed.filter((id) => live.instances.has(id));
		if (kept.length !== prefs.serveDismissed.length) prefs.serveDismissed = kept;
	});

	let deleting = $state<Slot | undefined>(undefined);
	let deleteOpen = $state(false);

	// A slot deleted while its editor is open closes the panel instead of leaving an empty state behind
	let shownSlot = $state<string | undefined>(undefined);
	$effect(() => {
		if (editing) shownSlot = editing.id;
		else if (slotParam === null) shownSlot = undefined;
		else if (live.ready && shownSlot === slotParam) close();
	});

	const launchStart = $derived.by((): LaunchStart => {
		const q = page.url.searchParams;
		const swap = q.get('swap');
		const model = q.get('model');
		const context = q.get('context');
		return {
			slotId: swap ?? q.get('slot') ?? undefined,
			swap: swap !== null,
			model: model ? splitKey(model) : undefined,
			runtimeId: q.get('runtime') ?? undefined,
			params: context ? { n_ctx: context } : undefined,
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

{#snippet launchTools()}
	{#if slots.length + loose.length > 0}
		<span class="filter"><MultiSelect bind:value={filters} options={filterOptions} label="Filter" compact /></span>
	{/if}
	<span class="fill"></span>
	<NeoButton text href={slotHref('new')}>{#snippet icon()}<PlusIcon size={16} />{/snippet}Slot</NeoButton>
	<NeoButton class="primary" elevation={2} href="/serve/launch">{#snippet icon()}<PlayIcon size={16} />{/snippet}Launch</NeoButton>
{/snippet}

<!-- Slots, empty or holding a model, then the models running outside any slot, in one table -->
{#snippet servingCard()}
	<Card tools={launchTools} flush>
		<List head={{ primary: 'Name', secondary: 'Model' }}>
			<NeoSortableProvider bind:items={sortItems} axis="y" onDragEnd={() => void reordered()}>
				{#each sortItems as item, index (item.id)}
					{@const slot = slotById.get(String(item.id))}
					{#if slot && shownSlots.has(slot.id)}
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
			{#each shownLoose as inst (inst.id)}
				<InstanceRow instance={inst} {slim} ondismiss={dismiss} selected={inst.id === instanceId} />
			{/each}
		</List>
		{#if shownSlots.size + shownLoose.length === 0}
			<Empty text="No instances match" action="Clear filter" onclick={() => (filters = [])} />
		{/if}
	</Card>
{/snippet}

{#snippet formationsCard()}
	<Card title="Formations" flush>
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
		{#if !live.ready}
			<Skeleton rows={2} kind="bars" />
		{:else}
			<MemoryBars profile={live.host} instances={live.instances.values()} slots={live.slots.values()} />
		{/if}
	</Card>
{/snippet}

{#snippet list()}
	{@render memoryCard()}
	{#if !live.ready}
		<Card flush>
			<Skeleton rows={3} leading="20px" secondary="40ch" />
		</Card>
	{:else if slots.length === 0 && loose.length === 0}
		<Card tools={launchTools}>
			<Empty icon={PlayIcon} text="Nothing serving" />
		</Card>
	{:else}
		{@render servingCard()}
	{/if}
	{#if formations.length}{@render formationsCard()}{/if}
{/snippet}

<PageHeader tabs={serveTabs} active="instances" />

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
	<NameConfirm bind:open={deleteOpen} title="Delete slot" name={deleting.name} message={deleting.instanceId ? 'Stops the model running in it' : undefined} onconfirm={deleteSlot} />
{/if}

{#if launching}
	<Launch start={launchStart} />
{/if}

<style>
	.filter {
		display: inline-flex;
		width: 14rem;
		max-width: 100%;
	}
	.sortrow {
		display: grid;
		grid-template-columns: subgrid;
		grid-column: 1 / -1;
	}
	.sortrow {
		border-bottom: 1px solid var(--nebu-border);
	}
	.sortrow:last-child {
		border-bottom-color: transparent;
	}
</style>
