<script lang="ts">
	import { goto } from '$app/navigation';
	import RowAction from '#lib/ui/RowAction.svelte';
	import { NeoProgressBar } from '#lib/neo/progress/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, enumLabel, num, pct } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { SlotState, SlotStateSchema, type Slot } from '#lib/proto/nebu/v1/slot_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import ListRow from '#lib/ui/ListRow.svelte';
	import type { MenuItem } from '#lib/ui/Menu.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { deviceLabel } from '#lib/ui/memory.js';
	import { slotLamp, slotShape } from '#lib/ui/status.js';
	import { canaryShare, canaryTimeLeft, modelPhrase, policyRows, slotHref, slotInFlight } from '#lib/pages/serve/serve.js';
	import DotsSixVerticalIcon from 'phosphor-svelte/lib/DotsSixVerticalIcon';

	let {
		slot,
		ondelete,
		handle,
		selected = false,
		slim = false
	}: {
		slot: Slot;
		ondelete: (s: Slot) => void;
		// Attaches the drag handle of the sortable list the row sits in
		handle: (node: HTMLElement) => () => void;
		selected?: boolean;
		// Whether the row shows the lamp, name and action alone, beside an open panel
		slim?: boolean;
	} = $props();

	const inst = $derived(live.instances.get(slot.instanceId));
	const failed = $derived(slot.state === SlotState.FAILED);
	const occupied = $derived(slot.instanceId !== '');
	const inFlight = $derived(slotInFlight(live, slot));
	const occupant = $derived(failed && slot.error ? slot.error : slot.state === SlotState.EMPTY ? 'empty' : modelPhrase(inst ?? slot.request));
	const devices = $derived(
		slot.deviceIds.map((id) => {
			const d = live.host?.devices.find((x) => x.id === id);
			return d ? deviceLabel(live.host, d) : id;
		})
	);
	// Runtime, devices, reservation, aliases and the policy fields the slot sets
	const tip = $derived(
		[
			`runtime ${slot.runtimeId || 'any'}`,
			`devices ${devices.join(', ') || 'any'}`,
			`memory ${slot.memoryBytes ? bytes(slot.memoryBytes) : 'no cap'}`,
			`aliases ${slot.aliases.map((a) => a.name).join(', ') || 'none'}`,
			...policyRows(slot.policy).map((r) => `${r.label} ${r.value}`)
		].join('\n')
	);
	const swapping = $derived(slot.state === SlotState.SWAPPING);
	const swapTask = $derived(swapping ? live.tasks.get(slot.taskId) : undefined);
	const share = $derived(swapping ? canaryShare(live, slot) : undefined);
	const canaryLine = $derived(slot.conditions.find((c) => c.type === 'Canary')?.message || swapTask?.progress?.message || '');
	const timeLeft = $derived(share === undefined ? '' : canaryTimeLeft(swapTask, clockTick.now));

	async function relaunch() {
		try {
			const res = await rpc.slots.relaunchSlot({ id: slot.id });
			followTask(res.task, `Relaunch ${slot.name}`);
		} catch (err) {
			toast.error('Relaunch failed', reason(err));
		}
	}

	async function evict() {
		try {
			await rpc.slots.evictSlot({ id: slot.id });
			toast.success(`Evicted ${slot.name}`);
		} catch (err) {
			toast.error('Evict failed', reason(err));
		}
	}

	// An occupied slot opens its instance, an empty one its settings
	function open() {
		void goto(occupied ? `/serve/${encodeURIComponent(slot.instanceId)}` : slotHref(slot.id));
	}

	const menu = $derived.by((): MenuItem[] => [
		{ label: 'Edit', href: slotHref(slot.id) },
		{ label: 'Logs', href: `/serve/${encodeURIComponent(slot.instanceId)}?tab=logs`, disabled: !occupied },
		{ label: 'Evict', confirm: `Evict ${slot.name}?`, onclick: evict, danger: true, disabled: !occupied },
		{ label: 'Delete', onclick: () => ondelete(slot), danger: true, divider: true }
	]);
</script>

{#snippet canary()}
	<Tip text={canaryLine} tag="div" class="canary">
		<span class="track"><NeoProgressBar value={share} elevation={-1} height="4px" width="100%" color="warning" immediate /></span>
		<span class="secondary nowrap"><span class="num">{pct(share)}</span> new, {timeLeft} left</span>
	</Tip>
{/snippet}

<ListRow
	lamp={slotLamp(slot.state)}
	shape={slotShape(slot.state)}
	primary={slot.name}
	primaryTip={tip}
	secondary={slim ? undefined : occupant}
	secondaryKind={failed ? 'message' : 'repo'}
	secondaryColor={failed ? 'error' : 'muted'}
	facts={slim ? [] : [{ value: inFlight !== undefined && occupied ? num(inFlight) : '', tip: 'in flight' }]}
	{menu}
	{selected}
	onclick={open}
	below={share === undefined ? undefined : canary}
>
	{#snippet leading()}
		<span class="handle" aria-label="Drag to reorder" {@attach handle}><DotsSixVerticalIcon size={20} /></span>
	{/snippet}
	{#snippet action()}
		{#if slot.state === SlotState.READY}
			<RowAction onclick={() => goto(`/serve/launch?swap=${slot.id}`)} label="Swap" />
		{:else if slot.state === SlotState.EMPTY}
			<RowAction onclick={() => goto(`/serve/launch?slot=${slot.id}`)} label="Launch" />
		{:else if failed || slot.state === SlotState.IDLE}
			<RowAction onclick={relaunch} label="Relaunch" />
		{:else}
			<span class="secondary">{enumLabel(SlotStateSchema, slot.state)}</span>
		{/if}
	{/snippet}
</ListRow>

<style>
	.handle {
		display: inline-flex;
		color: var(--nebu-text-faint);
		cursor: grab;
		touch-action: none;
	}
	/* The canary bar lines up with the row's name column, past the handle and the lamp */
	:global(.canary) {
		display: flex;
		flex: 1 1 auto;
		gap: var(--nebu-space-3);
		align-items: center;
		min-width: 0;
		padding-left: calc(var(--nebu-space-7) + var(--nebu-space-1));
	}
	.track {
		display: flex;
		flex: 1 1 auto;
		min-width: 0;
	}
</style>
