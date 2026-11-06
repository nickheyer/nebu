<script lang="ts">
	import { clone, create, toJsonString } from '@bufbuild/protobuf';
	import { onMount, untrack } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import Collapse from '#lib/ui/Collapse.svelte';
	import { reason, rpc } from '#lib/api/transport.js';
	import { live } from '#lib/live/events.svelte.js';
	import { Placement } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { PolicySchema, ProfileSchema } from '#lib/proto/nebu/v1/gateway_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { SlotAliasSchema, SlotState, type Slot } from '#lib/proto/nebu/v1/slot_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Confirm from '#lib/ui/Confirm.svelte';
	import NameConfirm from '#lib/ui/NameConfirm.svelte';
	import Bytes from '#lib/ui/fields/Bytes.svelte';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { deviceLabel } from '#lib/ui/memory.js';
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
	let force = $state(false);
	let forceOpen = $state(false);
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
	const dirty = $derived(!editing || snapshot() !== initial);

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
			.map((r) => ({ value: r.runtime!.id, label: r.runtime!.name || r.runtime!.id, description: r.runtime!.description }))
	);
	const runtimeParams = $derived(runtimes.find((r) => r.runtime?.id === runtimeId)?.runtime?.params ?? []);
	const devices = $derived((live.host?.devices ?? []).map((d) => ({ value: d.id, label: `${deviceLabel(live.host, d)} ${d.name}`, description: d.poolId })));
	const total = $derived.by(() => {
		const pools = new Set((live.host?.devices ?? []).filter((d) => deviceIds.includes(d.id)).map((d) => d.poolId));
		return (live.host?.pools ?? []).filter((p) => pools.has(p.id)).reduce((n, p) => n + Number(p.totalBytes), 0);
	});
	const occupied = $derived(slot !== undefined && slot.instanceId !== '' && slot.state !== SlotState.EMPTY);

	function addAlias() {
		aliases = [...aliases, create(SlotAliasSchema, { policy: create(PolicySchema), profile: create(ProfileSchema) })];
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
			await rpc.slots.deleteSlot({ id: slot.id, force });
			toast.success(`Deleted ${slot.name}`);
			onclose();
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}
</script>

<PanelHeader title={editing ? (slot?.name ?? '') : 'New slot'} {onclose} />
<Stack gap={24}>
	<FormSection title="Slot">
		<FieldRow>
			<Text bind:value={name} label="Name" max={64} required />
			<Text bind:value={description} label="Description" max={140} />
		</FieldRow>
		<FieldRow>
			<Checklist bind:value={deviceIds} options={devices} label="Devices" empty="No devices probed" />
			<Bytes bind:value={memoryBytes} total={total > 0 ? total : undefined} label="Memory" info="Reserved on the chosen devices" />
		</FieldRow>
		<FieldRow>
			<Select bind:value={runtimeId} options={runtimeOptions} label="Runtime" clearable placeholder="Any installed runtime" />
			<Segmented bind:value={placement} options={[{ value: Placement.UNSPECIFIED, label: 'Device first' }, { value: Placement.DEVICE, label: 'Device only' }, { value: Placement.HOST, label: 'Host only' }]} label="Placement" />
		</FieldRow>
	</FormSection>
	{#if runtimeParams.length}
		<FormSection title="Default parameters" info="Request parameters override these at launch">
			<ParamsForm params={runtimeParams} bind:values={params} bind:auto />
		</FormSection>
	{/if}
	<PolicyFields bind:policy bind:profile />
	<FormSection title="Aliases" info="Extra public names, each a route serving the slot's model">
		{#each aliases as alias, i (i)}
			<Stack gap={8}>
				<Row gap={8} wrap={false}>
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
			<NeoButton elevation={2} onclick={addAlias}>
				{#snippet icon()}<PlusIcon size={20} />{/snippet}
				Alias
			</NeoButton>
		</Row>
	</FormSection>
	<FormSection title="Launch">
		<FieldRow>
			<Text bind:value={args} label="Arguments" info="Extra words for the runtime command, split like a shell" max={512} mono />
			<NumberField bind:value={position} min={0} label="Position" info="Place in the slot list, zero puts it last" placeholder="end" />
		</FieldRow>
	</FormSection>
	{#if editing && slot}
		<FormSection title="Delete">
			<Row>
				{#if occupied}
					<Cell min="12rem"><Switch bind:checked={force} label="Stop the occupant" /></Cell>
				{/if}
				<Cell grow={false} push>
					{#if force}
						<NeoButton elevation={2} color="error" onclick={() => (forceOpen = true)}>Delete slot</NeoButton>
					{:else}
						<Confirm label="Delete slot" question="Delete {slot.name}?" danger disabled={occupied} small={false} onconfirm={remove} />
					{/if}
				</Cell>
			</Row>
		</FormSection>
		<NameConfirm bind:open={forceOpen} title="Delete slot" name={slot.name} message="The occupant stops first" onconfirm={remove} />
	{/if}
</Stack>
<SaveBar {dirty} {saving} {error} label={editing ? 'Save' : 'Create'} disabled={!name} onsave={save} ondiscard={onclose} />
