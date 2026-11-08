<script lang="ts">
	import { clone, create, toJsonString } from '@bufbuild/protobuf';
	import { onMount, untrack } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import Collapse from '#lib/ui/Collapse.svelte';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, enumLabel } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { Placement } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { PolicySchema, ProfileSchema } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { PoolKind } from '#lib/proto/nebu/v1/host_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { SlotAliasSchema, SlotStateSchema, type Slot } from '#lib/proto/nebu/v1/slot_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Confirm from '#lib/ui/Confirm.svelte';
	import NameConfirm from '#lib/ui/NameConfirm.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Bytes from '#lib/ui/fields/Bytes.svelte';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { deviceLabel } from '#lib/ui/memory.js';
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
	let deviceIds = $state<string[]>(draft?.deviceIds ?? []);
	let memoryBytes = $state(Number(draft?.memoryBytes ?? 0n));
	let runtimeId = $state(draft?.runtimeId ?? '');
	let params = $state<Record<string, string>>({ ...(draft?.params ?? {}) });
	let auto = $state<Record<string, boolean>>({});
	let policy = $state(draft?.policy ? clone(PolicySchema, draft.policy) : create(PolicySchema));
	let profile = $state(draft?.profile ? clone(ProfileSchema, draft.profile) : create(ProfileSchema));
	let placement = $state<Placement>(draft?.placement ?? Placement.UNSPECIFIED);
	let aliases = $state((draft?.aliases ?? []).map((a) => clone(SlotAliasSchema, a)));
	let args = $state(draft?.args ?? '');
	let position = $state(draft?.position ?? 0);
	let saving = $state(false);
	let error = $state<string | undefined>(undefined);
	let deleteOpen = $state(false);
	let runtimes = $state<RuntimeStatus[]>([]);

	// The form as one string, compared with the first render to tell a dirty form
	function snapshot(): string {
		return JSON.stringify([
			name,
			description,
			deviceIds,
			memoryBytes,
			runtimeId,
			params,
			toJsonString(PolicySchema, policy),
			toJsonString(ProfileSchema, profile),
			placement,
			aliases.map((a) => toJsonString(SlotAliasSchema, a)),
			args,
			position
		]);
	}
	const initial = untrack(snapshot);
	const dirty = $derived(snapshot() !== initial);

	onMount(async () => {
		try {
			runtimes = (await rpc.runtimes.listRuntimes({})).runtimes;
		} catch (err) {
			toast.error('Runtimes failed to list', reason(err));
		}
	});

	const installed = $derived(new Set([...live.installs.values()].map((i) => i.runtimeId)));
	const runtimeOptions = $derived(
		runtimes
			.filter((r) => r.runtime && (installed.has(r.runtime.id) || r.runtime.id === runtimeId))
			.map((r) => ({ value: r.runtime!.id, label: r.runtime!.name || r.runtime!.id, tip: r.runtime!.description || undefined }))
	);
	const runtimeParams = $derived(runtimes.find((r) => r.runtime?.id === runtimeId)?.runtime?.params ?? []);
	const devices = $derived(
		(live.host?.devices ?? []).map((d) => ({
			value: d.id,
			label: `${deviceLabel(live.host, d)} ${d.name}`,
			description: memoryOf(d.poolId)
		}))
	);
	// The pools the daemon caps at the slot's memory, devices the slot may use or host memory when host only
	const capped = $derived.by(() => {
		const hostOnly = placement === Placement.HOST;
		return (live.host?.pools ?? []).filter((p) => {
			const device = p.kind === PoolKind.DEVICE || p.kind === PoolKind.UNIFIED;
			if (device && deviceIds.length > 0 && !deviceIds.includes(p.deviceId) && !deviceIds.includes(p.id)) return false;
			return hostOnly ? p.kind === PoolKind.HOST || p.kind === PoolKind.UNIFIED : device;
		});
	});
	const capTotal = $derived(Math.max(0, ...capped.map((p) => Number(p.totalBytes))));
	const occupied = $derived(slot !== undefined && slot.instanceId !== '');

	// The memory a device holds, its pool's total, or the note that it borrows system memory
	function memoryOf(poolId: string): string {
		const pool = live.host?.pools.find((p) => p.id === poolId);
		return pool ? bytes(pool.totalBytes) : 'shares system memory';
	}

	function addAlias() {
		aliases = [...aliases, create(SlotAliasSchema, { policy: create(PolicySchema), profile: create(ProfileSchema) })];
	}

	// Zero leaves memory uncapped, which reads as the whole of the largest pool the slot may fill
	function setMemory(n: number) {
		memoryBytes = n >= capTotal ? 0 : n;
	}

	async function save() {
		saving = true;
		error = undefined;
		try {
			const common = { description, deviceIds, memoryBytes: BigInt(memoryBytes), runtimeId, params, policy, profile, placement, aliases, args, position };
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

	async function remove() {
		if (!slot) return;
		try {
			await rpc.slots.deleteSlot({ id: slot.id, force: occupied });
			toast.success(`Deleted ${slot.name}`);
			onclose();
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}
</script>

{#snippet placementField()}
	<Segmented
		bind:value={placement}
		options={[
			{ value: Placement.UNSPECIFIED, label: 'Auto', tip: 'Devices first, spilling into host memory' },
			{ value: Placement.DEVICE, label: 'Device', tip: 'Device memory alone, never spilling' },
			{ value: Placement.HOST, label: 'Host', tip: 'Host memory alone' }
		]}
		label="Placement"
	/>
{/snippet}

{#snippet memoryField()}
	{#if capTotal > 0}
		<Bytes bind:value={() => memoryBytes || capTotal, setMemory} total={capTotal} label="Memory" info="The most the model may hold in each pool it uses" message={memoryBytes === 0 ? 'No cap' : `${bytes(memoryBytes)} of ${bytes(capTotal)}`} />
	{:else}
		<Bytes value={0} total={Math.max(0, ...(live.host?.pools ?? []).map((p) => Number(p.totalBytes)))} label="Memory" info="The most the model may hold in each pool it uses" disabled message="Nothing this placement caps" />
	{/if}
{/snippet}

<PanelHeader title={editing ? (slot?.name ?? '') : 'New slot'} subtitle={slot?.description || undefined} {onclose}>
	{#if slot}<StatePill label={enumLabel(SlotStateSchema, slot.state)} lamp={slotLamp(slot.state)} />{/if}
	{#snippet tools()}
		{#if slot}
			{#if occupied}
				<NeoButton flat color="error" onclick={() => (deleteOpen = true)}>Delete</NeoButton>
			{:else}
				<Confirm label="Delete" question="Delete {slot.name}?" danger onconfirm={remove} />
			{/if}
		{/if}
	{/snippet}
</PanelHeader>
<Stack gap={24}>
	<FormSection>
		<FieldRow>
			<Text bind:value={name} label="Name" max={64} required />
			<Text bind:value={description} label="Description" max={140} />
		</FieldRow>
	</FormSection>
	<FormSection title="Resources">
		<Checklist bind:value={deviceIds} options={devices} label="Devices" info="None chosen lets the planner use any" empty="No devices probed" />
		<FieldRow>
			{@render placementField()}
			{@render memoryField()}
		</FieldRow>
	</FormSection>
	<PolicyFields bind:policy bind:profile />
	<FormSection title="Aliases" info="Extra public names, each a route serving the slot's model">
		{#each aliases as alias, i (i)}
			<Stack gap={8}>
				<Row gap={8} wrap={false} align="flex-end">
					<Cell min="12rem"><Text bind:value={alias.name} label="Alias" max={64} /></Cell>
					<Cell grow={false}>
						<NeoButton text aria-label="Remove alias" onclick={() => (aliases = aliases.filter((_, j) => j !== i))}>
							{#snippet icon()}<XIcon size={20} />{/snippet}
						</NeoButton>
					</Cell>
				</Row>
				<Collapse label="Policy and tools">
					{#if alias.policy && alias.profile}
						<PolicyFields bind:policy={alias.policy} bind:profile={alias.profile} />
					{/if}
				</Collapse>
			</Stack>
		{/each}
		<Row>
			<NeoButton text onclick={addAlias}>
				{#snippet icon()}<PlusIcon size={16} />{/snippet}
				Add alias
			</NeoButton>
		</Row>
	</FormSection>
	<FormSection title="Launch">
		<FieldRow>
			<Select bind:value={runtimeId} options={runtimeOptions} label="Runtime" info="Its parameters below are the slot's defaults, launch requests override them" clearable placeholder="Any installed" />
			<NumberField bind:value={position} min={0} label="Position" info="Place in the slot list and relaunch order, the end by default" emptyZero placeholder="Default" />
		</FieldRow>
		<Text bind:value={args} label="Arguments" info="Extra words for the runtime command, split like a shell" max={512} mono />
	</FormSection>
	{#if runtimeParams.length}
		<ParamsForm params={runtimeParams} bind:values={params} bind:auto />
	{/if}
</Stack>
<SaveBar always={!editing} {dirty} {saving} {error} message={name.trim() ? undefined : 'Name needed'} label={editing ? 'Save' : 'Create'} disabled={!name.trim()} onsave={save} ondiscard={onclose} />
{#if slot && occupied}
	<NameConfirm bind:open={deleteOpen} title="Delete slot" name={slot.name} message="The occupant stops first" onconfirm={remove} />
{/if}
