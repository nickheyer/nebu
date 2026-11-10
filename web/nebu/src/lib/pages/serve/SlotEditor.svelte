<script lang="ts">
	import { clone, create, toJsonString } from '@bufbuild/protobuf';
	import { onMount, untrack } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { enumLabel } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { Placement } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { PolicySchema, ProfileSchema } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { PoolKind, type HostProfile } from '#lib/proto/nebu/v1/host_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { SlotAliasSchema, SlotStateSchema, type Slot } from '#lib/proto/nebu/v1/slot_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Confirm from '#lib/ui/Confirm.svelte';
	import NameConfirm from '#lib/ui/NameConfirm.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import PoolMemory from '#lib/ui/fields/PoolMemory.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { slotLamp } from '#lib/ui/status.js';
	import ParamsForm from '#lib/pages/serve/ParamsForm.svelte';
	import PolicyFields from '#lib/pages/serve/PolicyFields.svelte';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import XIcon from 'phosphor-svelte/lib/XIcon';

	let { slot, onclose }: { slot?: Slot; onclose: () => void } = $props();

	const draft = untrack(() => slot);
	const editing = draft !== undefined;
	let name = $state(draft?.name ?? '');
	let description = $state(draft?.description ?? '');
	let runtimeId = $state(draft?.runtimeId ?? '');
	let params = $state<Record<string, string>>({ ...(draft?.params ?? {}) });
	let policy = $state(draft?.policy ? clone(PolicySchema, draft.policy) : create(PolicySchema));
	let profile = $state(draft?.profile ? clone(ProfileSchema, draft.profile) : create(ProfileSchema));
	let aliases = $state((draft?.aliases ?? []).map((a) => clone(SlotAliasSchema, a)));
	let args = $state(draft?.args ?? '');
	// Bytes the slot may take from each of this host's pools, read from the slot once the host is known
	let limits = $state<Record<string, number> | undefined>(undefined);
	let limitsAtOpen = '';
	let saving = $state(false);
	let error = $state<string | undefined>(undefined);
	let deleteOpen = $state(false);
	let runtimes = $state<RuntimeStatus[]>([]);

	function snapshot(): string {
		return JSON.stringify([name, description, runtimeId, params, toJsonString(PolicySchema, policy), toJsonString(ProfileSchema, profile), aliases.map((a) => toJsonString(SlotAliasSchema, a)), args]);
	}
	const initial = untrack(snapshot);
	const dirty = $derived(snapshot() !== initial || (limits !== undefined && JSON.stringify(limits) !== limitsAtOpen));

	onMount(async () => {
		try {
			runtimes = (await rpc.runtimes.listRuntimes({})).runtimes;
		} catch (err) {
			toast.error('Runtimes failed to list', reason(err));
		}
	});

	const selfId = $derived([...live.nodes.values()].find((n) => n.self)?.id ?? '');

	// A device or pool id as this host names it, nothing for one on another node
	function local(id: string): string | undefined {
		if (!id.includes('/')) return id;
		return selfId !== '' && id.startsWith(`${selfId}/`) ? id.slice(selfId.length + 1) : undefined;
	}

	// What the stored devices, placement, budget and pool caps leave open in each pool of this host
	function read(host: HostProfile): Record<string, number> {
		const pins = draft?.deviceIds ?? [];
		const here = pins.map(local).filter((id) => id !== undefined);
		const caps = new Map(Object.entries(draft?.poolBytes ?? {}).map(([k, v]) => [local(k), Number(v)]));
		const budget = Number(draft?.memoryBytes ?? 0n);
		const placement = draft?.placement ?? Placement.UNSPECIFIED;
		const out: Record<string, number> = {};
		for (const p of host.pools) {
			let n = Number(p.totalBytes);
			const device = p.kind === PoolKind.DEVICE || p.kind === PoolKind.UNIFIED;
			const hostSide = p.kind === PoolKind.HOST || p.kind === PoolKind.UNIFIED;
			const unpinned = pins.length > 0 && !here.includes(p.deviceId) && !here.includes(p.id);
			if ((p.kind === PoolKind.DEVICE && (placement === Placement.HOST || unpinned)) || (p.kind === PoolKind.HOST && placement === Placement.DEVICE)) n = 0;
			if (n > 0 && budget > 0 && (placement === Placement.HOST ? hostSide : device)) n = Math.min(n, budget);
			if (n > 0) n = Math.min(n, caps.get(p.id) ?? n);
			out[p.id] = n;
		}
		return out;
	}

	$effect(() => {
		const host = live.host;
		if (!host || limits !== undefined || (live.inMesh && selfId === '')) return;
		untrack(() => {
			limits = read(host);
			limitsAtOpen = JSON.stringify(limits);
		});
	});

	// The devices, placement and pool caps the sliders stand for, pins and caps on other nodes kept as they are
	function resources(host: HostProfile, open: Record<string, number>) {
		const has = (id: string) => (open[id] ?? 0) > 0;
		const devices = host.pools.filter((p) => p.kind === PoolKind.DEVICE);
		const hosts = host.pools.filter((p) => p.kind === PoolKind.HOST);
		const openDevices = devices.filter((p) => has(p.id));
		const farPins = (draft?.deviceIds ?? []).filter((id) => local(id) === undefined);
		let placement = Placement.UNSPECIFIED;
		if (devices.length > 0 && openDevices.length === 0 && farPins.length === 0) placement = Placement.HOST;
		else if (hosts.length > 0 && !hosts.some((p) => has(p.id))) placement = Placement.DEVICE;
		const every = openDevices.length === devices.length && farPins.length === 0;
		const deviceIds = placement === Placement.HOST || every ? [] : [...openDevices.map((p) => p.deviceId || p.id), ...farPins];
		const poolBytes: Record<string, bigint> = Object.fromEntries(Object.entries(draft?.poolBytes ?? {}).filter(([k]) => local(k) === undefined));
		for (const p of host.pools) {
			const n = open[p.id] ?? 0;
			if (n > 0 && n < Number(p.totalBytes)) poolBytes[p.id] = BigInt(Math.round(n));
		}
		return { placement, deviceIds, poolBytes, memoryBytes: 0n };
	}

	const closed = $derived(limits !== undefined && Object.values(limits).every((n) => n <= 0));
	const installed = $derived(new Set([...live.installs.values()].map((i) => i.runtimeId)));
	const runtimeOptions = $derived(
		runtimes
			.filter((r) => r.runtime && (installed.has(r.runtime.id) || r.runtime.id === runtimeId))
			.map((r) => ({ value: r.runtime!.id, label: r.runtime!.name || r.runtime!.id, tip: r.runtime!.description || undefined }))
	);
	const runtimeParams = $derived(runtimes.find((r) => r.runtime?.id === runtimeId)?.runtime?.params ?? []);
	const occupied = $derived(slot !== undefined && slot.instanceId !== '');
	const others = $derived(
		[...live.slots.values()]
			.filter((s) => s.id !== draft?.id)
			.sort((a, b) => a.position - b.position || a.name.localeCompare(b.name))
			.map((s) => ({ value: s.id, label: s.name }))
	);

	function addAlias() {
		aliases = [...aliases, create(SlotAliasSchema)];
	}

	async function save() {
		saving = true;
		error = undefined;
		try {
			const held = { placement: draft?.placement ?? Placement.UNSPECIFIED, deviceIds: draft?.deviceIds ?? [], poolBytes: draft?.poolBytes ?? {}, memoryBytes: draft?.memoryBytes ?? 0n };
			const common = { description, runtimeId, params, policy, profile, aliases, args, position: draft?.position ?? 0, ...(live.host && limits ? resources(live.host, limits) : held) };
			if (editing && slot) await rpc.slots.updateSlot({ id: slot.id, name: name === slot.name ? '' : name, ...common });
			else await rpc.slots.createSlot({ name, ...common });
			toast.success(editing ? `Saved ${name}` : `Created ${name}`);
			onclose();
		} catch (err) {
			error = reason(err);
		} finally {
			saving = false;
		}
	}

	// Deletes the slot the editor opened on, which the stream drops from the live mirror before the call returns
	async function remove() {
		if (!draft) return;
		try {
			await rpc.slots.deleteSlot({ id: draft.id, force: occupied });
			toast.success(`Deleted ${draft.name}`);
			onclose();
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}
</script>

{#snippet deleteButton()}
	{#if slot}
		{#if occupied}
			<NeoButton text class="danger" onclick={() => (deleteOpen = true)}>Delete</NeoButton>
		{:else}
			<Confirm label="Delete" question="Delete {slot.name}?" danger placement="top-start" onconfirm={remove} />
		{/if}
	{/if}
{/snippet}

<PanelHeader title={editing ? (slot?.name ?? '') : 'New slot'} subtitle={slot?.description || undefined} {onclose}>
	{#if slot}<StatePill label={enumLabel(SlotStateSchema, slot.state)} lamp={slotLamp(slot.state)} />{/if}
</PanelHeader>
<Stack gap={24}>
	<FormSection>
		<FieldRow>
			<Text bind:value={name} label="Name" max={64} required />
			<Text bind:value={description} label="Description" max={140} />
		</FieldRow>
	</FormSection>
	{#if limits}
		<FormSection title="Memory" info="How much of each a model in the slot may use">
			<PoolMemory bind:value={limits} profile={live.host} />
			{#if closed}<p class="error-text secondary">Leave memory open in at least one</p>{/if}
		</FormSection>
	{/if}
	<PolicyFields bind:policy bind:profile />
	<FormSection title="Aliases" info="Other names clients can call the slot by">
		{#each aliases as alias, i (i)}
			<Row gap={8} wrap={false} align="flex-end">
				<Cell min="10rem"><Text bind:value={alias.name} label={i === 0 ? 'Name' : undefined} max={64} /></Cell>
				{#if others.length}
					<Cell min="10rem">
						<Select bind:value={() => alias.inheritSlotId || undefined, (v) => (alias.inheritSlotId = v ?? '')} options={others} label={i === 0 ? 'Limits and tools of' : undefined} placeholder={name} clearable />
					</Cell>
				{/if}
				<Cell grow={false}>
					<span class="trail">
						<NeoButton text aria-label="Remove alias" onclick={() => (aliases = aliases.filter((_, j) => j !== i))}>
							{#snippet icon()}<XIcon size={20} />{/snippet}
						</NeoButton>
					</span>
				</Cell>
			</Row>
		{/each}
		<Row>
			<NeoButton text onclick={addAlias}>
				{#snippet icon()}<PlusIcon size={16} />{/snippet}
				Alias
			</NeoButton>
		</Row>
	</FormSection>
	<FormSection title="Launch">
		<FieldRow>
			<Select bind:value={runtimeId} options={runtimeOptions} label="Runtime" clearable />
			<Text bind:value={args} label="Arguments" info="Added to the runtime's command" max={512} mono />
		</FieldRow>
	</FormSection>
	{#if runtimeParams.length}
		<ParamsForm params={runtimeParams} bind:values={params} />
	{/if}
</Stack>
<SaveBar always={!editing} {dirty} {saving} {error} label={editing ? 'Save' : 'Create'} disabled={!name.trim() || closed} leading={editing ? deleteButton : undefined} onsave={save} ondiscard={onclose} />
{#if slot && occupied}
	<NameConfirm bind:open={deleteOpen} title="Delete slot" name={slot.name} message="Stops the model running in it" onconfirm={remove} />
{/if}
