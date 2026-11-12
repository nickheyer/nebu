<script lang="ts">
	import { goto } from '$app/navigation';
	import { NeoProgressBar } from '#lib/neo/progress/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, pct } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { SlotState, type Slot } from '#lib/proto/nebu/v1/slot_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import ListRow from '#lib/ui/ListRow.svelte';
	import type { MenuItem } from '#lib/ui/Menu.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { deviceLabel } from '#lib/ui/memory.js';
	import { slotLamp, slotShape } from '#lib/ui/status.js';
	import { canaryShare, canaryTimeLeft, installLabel, modelPhrase, policyRows, slotHref } from '#lib/pages/serve/serve.js';
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
	const occupant = $derived(failed && slot.error ? slot.error : slot.state === SlotState.EMPTY ? '' : modelPhrase(inst ?? slot.request));
	const devices = $derived(
		slot.deviceIds.map((id) => {
			const d = live.host?.devices.find((x) => x.id === id);
			return d ? deviceLabel(live.host, d) : id;
		})
	);
	const pinned = $derived(slot.installId ? live.installs.get(slot.installId) : undefined);
	// Runtime, install, devices, memory, aliases and the limits the slot sets
	const tip = $derived(
		[
			slot.runtimeId ? `runtime ${slot.runtimeId}` : '',
			pinned ? `install ${installLabel(pinned)}` : '',
			devices.length ? `devices ${devices.join(', ')}` : '',
			slot.memoryBytes ? `memory ${bytes(slot.memoryBytes)}` : '',
			...Object.entries(slot.poolBytes).map(([pool, n]) => `${pool} ${bytes(n)}`),
			slot.aliases.length ? `aliases ${slot.aliases.map((a) => a.name).join(', ')}` : '',
			...policyRows(slot.policy).map((r) => `${r.label} ${r.value}`)
		]
			.filter(Boolean)
			.join('\n')
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

	// The move the slot's state allows comes first, launch while empty, swap while serving, relaunch after a failure or idle unload
	const menu = $derived.by((): MenuItem[] => {
		const items: MenuItem[] = [];
		if (slot.state === SlotState.EMPTY) items.push({ label: 'Launch', href: `/serve/launch?slot=${slot.id}` });
		else if (slot.state === SlotState.READY) items.push({ label: 'Swap', href: `/serve/launch?swap=${slot.id}` });
		else if (failed || slot.state === SlotState.IDLE) items.push({ label: 'Relaunch', onclick: relaunch });
		items.push(
			{ label: 'Edit', href: slotHref(slot.id), divider: items.length > 0 },
			{ label: 'Logs', href: `/serve/${encodeURIComponent(slot.instanceId)}?tab=logs`, disabled: !occupied },
			{ label: 'Evict', confirm: `Evict ${slot.name}?`, onclick: evict, danger: true, disabled: !occupied },
			{ label: 'Delete', onclick: () => ondelete(slot), danger: true, divider: true }
		);
		return items;
	});
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
	{menu}
	{selected}
	onclick={open}
	below={share === undefined ? undefined : canary}
>
	{#snippet leading()}
		<span class="handle" aria-label="Drag to reorder" {@attach handle}><DotsSixVerticalIcon size={20} /></span>
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
