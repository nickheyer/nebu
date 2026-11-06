<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoSortableItem, NeoSortableProvider } from '#lib/neo/sortable/index.js';
	import type { NeoSortableItem as SortItem } from '#lib/neo/sortable/neo-sortable-context.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { date, enumLabel, plural } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { ShapeSchema } from '#lib/proto/nebu/v1/estimate_pb.js';
	import type { Slot } from '#lib/proto/nebu/v1/slot_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { prefs } from '#lib/state/prefs.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import MemoryBars from '#lib/ui/MemoryBars.svelte';
	import NameConfirm from '#lib/ui/NameConfirm.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { formationLamp } from '#lib/ui/status.js';
	import InstancePanel from '#lib/pages/serve/InstancePanel.svelte';
	import InstanceRow from '#lib/pages/serve/InstanceRow.svelte';
	import LaunchWizard, { type WizardStart } from '#lib/pages/serve/LaunchWizard.svelte';
	import SlotEditor from '#lib/pages/serve/SlotEditor.svelte';
	import SlotRow from '#lib/pages/serve/SlotRow.svelte';
	import { slotUpdate, splitKey, terminal } from '#lib/pages/serve/serve.js';
	import PlayIcon from 'phosphor-svelte/lib/PlayIcon';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';

	let { instanceId }: { instanceId: string } = $props();

	const launching = $derived(instanceId === 'launch');
	const selected = $derived(launching ? undefined : live.instances.get(instanceId));
	const panelOpen = $derived(!launching && instanceId !== '');
	const tab = $derived(page.url.searchParams.get('tab') ?? '');

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
	const formations = $derived([...live.formations.values()].sort((a, b) => a.name.localeCompare(b.name)));

	// Forgets dismissed ids the daemon no longer holds
	$effect(() => {
		if (!live.ready) return;
		const kept = prefs.serveDismissed.filter((id) => live.instances.has(id));
		if (kept.length !== prefs.serveDismissed.length) prefs.serveDismissed = kept;
	});

	let editing = $state<Slot | undefined>(undefined);
	let editorOpen = $state(false);
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

	function openEditor(slot?: Slot) {
		editing = slot;
		editorOpen = true;
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

{#snippet list()}
	<Stack gap={24}>
		<Section title="Memory">
			{#snippet tools()}
				<NeoButton elevation={2} onclick={() => openEditor()}>
					{#snippet icon()}<PlusIcon size={20} />{/snippet}
					Slot
				</NeoButton>
				<NeoButton class="primary" elevation={2} href="/serve/launch">
					{#snippet icon()}<PlayIcon size={20} />{/snippet}
					Launch
				</NeoButton>
			{/snippet}
			{#if !live.ready}
				<Skeleton rows={2} height={28} />
			{:else}
				<MemoryBars profile={live.host} instances={live.instances.values()} slots={live.slots.values()} />
			{/if}
		</Section>
		<Section title="Slots" count={slots.length}>
			{#if !live.ready}
				<Skeleton rows={3} />
			{:else if slots.length === 0}
				<Empty text="No slots" action="Add a slot" onclick={() => openEditor()} />
			{:else}
				<NeoSortableProvider bind:items={sortItems} axis="y" onDragEnd={() => void reordered()}>
					{#each sortItems as item, index (item.id)}
						{@const slot = slotById.get(String(item.id))}
						{#if slot}
							<NeoSortableItem id={item.id} {index} data={item.data}>
								{#snippet children({ instance })}
									<div {@attach instance.attach}>
										<SlotRow {slot} handle={instance.attachHandle} onedit={openEditor} ondelete={askDelete} />
									</div>
								{/snippet}
							</NeoSortableItem>
						{/if}
					{/each}
				</NeoSortableProvider>
			{/if}
		</Section>
		<Section title="Instances without a slot" count={loose.length}>
			{#if !live.ready}
				<Skeleton rows={1} />
			{:else if loose.length === 0}
				<Empty text="No instances without a slot" action="Launch" href="/serve/launch" />
			{:else}
				{#each loose as inst (inst.id)}
					<InstanceRow instance={inst} ondismiss={dismiss} />
				{/each}
			{/if}
		</Section>
		{#if live.inMesh}
			<Section title="Formations" count={formations.length}>
				{#if formations.length === 0}
					<Empty text="No formations" action="Launch" href="/serve/launch" />
				{:else}
					{#each formations as f (f.id)}
						{@const href = `/mesh/formations/${encodeURIComponent(f.id)}`}
						<ListRow lamp={formationLamp(f.state)} primary={f.name} secondary="{enumLabel(ShapeSchema, f.shape)} · {plural(nodeCount(f.seats), 'node')}" {href}>
							{#snippet action()}<NeoButton text {href}>Open</NeoButton>{/snippet}
						</ListRow>
					{/each}
				{/if}
			</Section>
		{/if}
	</Stack>
{/snippet}

<Sheet open={panelOpen} onclose={() => goto('/serve')} {list} empty="Select an instance" emptyAction="Launch" emptyHref="/serve/launch">
	{#if selected}
		{#key selected.id}
			<InstancePanel instance={selected} {tab} ontab={showTab} onclose={() => goto('/serve')} />
		{/key}
	{:else if live.ready}
		<Empty text="No such instance" action="Back" href="/serve" />
	{:else}
		<Skeleton rows={6} height={28} />
	{/if}
</Sheet>

<Sheet bind:open={editorOpen}>
	{#if editorOpen}
		<SlotEditor slot={editing} onclose={() => (editorOpen = false)} />
	{/if}
</Sheet>

{#if deleting}
	<NameConfirm bind:open={deleteOpen} title="Delete slot" name={deleting.name} message={deleting.instanceId ? 'The occupant stops first' : undefined} onconfirm={deleteSlot} />
{/if}

{#if launching}
	<LaunchWizard start={wizardStart} />
{/if}
