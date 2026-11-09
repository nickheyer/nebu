<script lang="ts">
	import { clone, create, toJsonString } from '@bufbuild/protobuf';
	import { onMount, untrack } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import Collapse from '#lib/ui/Collapse.svelte';
	import { reason, rpc } from '#lib/api/transport.js';
	import { enumLabel } from '#lib/format.js';
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
	import Devices from '#lib/ui/fields/Devices.svelte';
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
	// A new slot lands last, an edited one keeps its place while the box stays empty
	const positionFallback = $derived(String(editing ? (draft?.position ?? 0) : live.slots.size + 1));

	function addAlias() {
		aliases = [...aliases, create(SlotAliasSchema, { policy: create(PolicySchema), profile: create(ProfileSchema) })];
	}

	// Zero leaves memory uncapped
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

{#snippet placementField()}
	<Segmented
		bind:value={placement}
		options={[
			{ value: Placement.UNSPECIFIED, label: 'Device first' },
			{ value: Placement.DEVICE, label: 'Device' },
			{ value: Placement.HOST, label: 'Host' }
		]}
		label="Placement"
		info="Device memory first, device only, or host memory only"
	/>
{/snippet}

{#snippet memoryField()}
	<Bytes bind:value={() => memoryBytes || capTotal, setMemory} total={capTotal} label="Memory" info="Device memory the occupant may take, every byte when empty" blank={memoryBytes === 0} fallback={capTotal} />
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
			<Text bind:value={name} label="Name" info="The slot's route name" max={64} required />
			<Text bind:value={description} label="Description" info="A line under the name" max={140} />
		</FieldRow>
	</FormSection>
	<FormSection title="Resources" info="No device chosen leaves every device open to the occupant">
		<Devices bind:value={deviceIds} profile={live.host} label="Devices" info="Devices the occupant may use, every device when none is chosen" />
		{@render placementField()}
		{#if capTotal > 0}{@render memoryField()}{/if}
	</FormSection>
	<PolicyFields bind:policy bind:profile />
	<FormSection title="Aliases" info="Other route names that serve the slot's model, each with its own policy and tools">
		{#each aliases as alias, i (i)}
			<Stack gap={8}>
				<Row gap={8} wrap={false} align="flex-end">
					<Cell min="12rem"><Text bind:value={alias.name} label="Alias" info="Another route name serving the slot's model" max={64} /></Cell>
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
				Alias
			</NeoButton>
		</Row>
	</FormSection>
	<FormSection title="Launch" info="What a launch into the slot starts with">
		<FieldRow>
			<Select bind:value={runtimeId} options={runtimeOptions} label="Runtime" info="The runtime launches default to" placeholder="Chosen at launch" clearable />
			<NumberField bind:value={position} min={0} label="Position" info="Place in the list and relaunch order, last when empty" emptyZero placeholder={positionFallback} />
		</FieldRow>
		<Text bind:value={args} label="Arguments" info="Extra words for the server command, templates over the launch" max={512} mono />
	</FormSection>
	{#if runtimeParams.length}
		<ParamsForm params={runtimeParams} bind:values={params} />
	{/if}
</Stack>
<SaveBar always={!editing} {dirty} {saving} {error} label={editing ? 'Save' : 'Create'} disabled={!name.trim()} onsave={save} ondiscard={onclose} />
{#if slot && occupied}
	<NameConfirm bind:open={deleteOpen} title="Delete slot" name={slot.name} message="Stops the occupant" onconfirm={remove} />
{/if}
