<script lang="ts">
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoProgressBar } from '#lib/neo/progress/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, enumLabel, num, pct, uptime } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { SlotState, SlotStateSchema, type Slot } from '#lib/proto/nebu/v1/slot_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import type { MenuItem } from '#lib/ui/Menu.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { deviceLabel } from '#lib/ui/memory.js';
	import { slotLamp, slotShape } from '#lib/ui/status.js';
	import { canaryShare, canaryTimeLeft, modelPhrase, policyRows, slotInFlight } from '#lib/pages/serve/serve.js';
	import DotsSixVerticalIcon from 'phosphor-svelte/lib/DotsSixVerticalIcon';

	let {
		slot,
		onedit,
		ondelete,
		handle
	}: {
		slot: Slot;
		onedit: (s: Slot) => void;
		ondelete: (s: Slot) => void;
		// Attaches the drag handle of the sortable list the row sits in
		handle?: (node: HTMLElement) => () => void;
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
	const tip = $derived(
		[
			`runtime ${slot.runtimeId || 'any'}`,
			`devices ${devices.join(', ') || 'any'}`,
			`reserved ${slot.memoryBytes ? bytes(slot.memoryBytes) : 'none'}`,
			`aliases ${slot.aliases.map((a) => a.name).join(', ') || 'none'}`
		].join('\n')
	);
	const swapping = $derived(slot.state === SlotState.SWAPPING);
	const swapTask = $derived(swapping ? live.tasks.get(slot.taskId) : undefined);
	const share = $derived(swapping ? canaryShare(live, slot) : undefined);
	const canaryLine = $derived(slot.conditions.find((c) => c.type === 'Canary')?.message || swapTask?.progress?.message || '');
	const timeLeft = $derived(share === undefined ? '' : canaryTimeLeft(swapTask, clockTick.now));
	const expandRows = $derived([
		...policyRows(slot.policy),
		{ label: 'uptime', value: inst?.readyAt ? uptime(inst.readyAt, clockTick.now) : '' },
		{ label: 'context', value: inst?.inventory?.contextLength ? `${num(inst.inventory.contextLength)} tokens` : '' }
	]);

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

	const menu = $derived.by((): MenuItem[] => [
		{ label: 'Edit', onclick: () => onedit(slot) },
		{ label: 'Evict', confirm: `Evict ${slot.name}?`, onclick: evict, danger: true, disabled: !occupied },
		{ label: 'Logs', href: `/serve/${slot.instanceId}?tab=logs`, disabled: !occupied },
		{ label: 'Open instance', href: `/serve/${slot.instanceId}`, disabled: !occupied },
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
	secondary={occupant}
	secondaryKind={failed ? 'message' : 'repo'}
	secondaryColor={failed ? 'error' : 'muted'}
	facts={inFlight !== undefined && occupied ? [{ value: `${num(inFlight)} in flight` }] : []}
	{menu}
	href={occupied ? `/serve/${slot.instanceId}` : undefined}
	below={share === undefined ? undefined : canary}
>
	{#snippet leading()}
		{#if handle}<span class="handle" aria-label="Drag to reorder" {@attach handle}><DotsSixVerticalIcon size={20} /></span>{/if}
	{/snippet}
	{#snippet action()}
		{#if slot.state === SlotState.READY}
			<NeoButton text onclick={() => goto(`/serve/launch?swap=${slot.id}`)}>Swap</NeoButton>
		{:else if slot.state === SlotState.EMPTY}
			<NeoButton text onclick={() => goto(`/serve/launch?slot=${slot.id}`)}>Launch</NeoButton>
		{:else if failed || slot.state === SlotState.IDLE}
			<NeoButton text onclick={relaunch}>Relaunch</NeoButton>
		{:else}
			<span class="secondary">{enumLabel(SlotStateSchema, slot.state)}</span>
		{/if}
	{/snippet}
	{#snippet expand()}
		<KeyRows dense rows={expandRows} />
	{/snippet}
</ListRow>

<style>
	.handle {
		display: inline-flex;
		cursor: grab;
		touch-action: none;
	}
	:global(.canary) {
		display: flex;
		flex: 1 1 auto;
		gap: 12px;
		align-items: center;
		min-width: 0;
		padding-left: 52px;
	}
	.track {
		display: flex;
		flex: 1 1 auto;
		min-width: 0;
	}
</style>
