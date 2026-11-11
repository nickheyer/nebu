<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { untrack } from 'svelte';
	import { reason, rpc } from '#lib/api/transport.js';
	import { live } from '#lib/live/events.svelte.js';
	import { ObjectiveSchema, type Objective } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Conditions from '#lib/ui/Conditions.svelte';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';

	let { objective, onclose }: { objective?: Objective; onclose: () => void } = $props();

	const start = untrack(() => objective);
	const editing = start !== undefined;
	let name = $state(start?.name ?? '');
	let priority = $state(start?.priority ?? 0);
	let ttft = $state(start?.ttftMs ?? 0);
	let tpot = $state(start?.tpotMs ?? 0);
	let saving = $state(false);
	let error = $state<string | undefined>(undefined);

	const current = $derived(editing ? live.objectives.get(name) : undefined);
	const dirty = $derived(!editing || priority !== start?.priority || ttft !== start?.ttftMs || tpot !== start?.tpotMs);

	async function save() {
		saving = true;
		error = undefined;
		try {
			await rpc.gateway.putObjective({ objective: create(ObjectiveSchema, { name: name.trim(), priority, ttftMs: ttft, tpotMs: tpot }) });
			toast.success(`Saved ${name.trim()}`);
			onclose();
		} catch (err) {
			error = reason(err);
		} finally {
			saving = false;
		}
	}

	async function remove() {
		try {
			await rpc.gateway.deleteObjective({ name });
			toast.success(`Deleted ${name}`);
			onclose();
		} catch (err) {
			toast.error('Delete objective failed', reason(err));
		}
	}
</script>

<PanelHeader title={editing ? name : 'New objective'} {onclose} />
<Stack gap={24}>
	<FormSection>
		<FieldRow>
			<Text bind:value={name} label="Name" required readonly={editing} max={64} />
			<NumberField bind:value={priority} min={-1000} max={1000} label="Priority" info="Higher is turned away last under load" />
		</FieldRow>
		<FieldRow wide>
			<Duration bind:value={ttft} base="ms" max={3_600_000} label="Time to first token" emptyZero />
			<Duration bind:value={tpot} base="ms" max={3_600_000} label="Time per output token" emptyZero />
		</FieldRow>
	</FormSection>
	{#if current?.status?.conditions.length}
		<Section title="Status" level={3}>
			<Conditions conditions={current.status.conditions} />
		</Section>
	{/if}
</Stack>
{#snippet deleteButton()}
	<Confirm label="Delete" question="Delete {name}?" danger placement="top-start" onconfirm={remove} />
{/snippet}

<SaveBar {dirty} always={!editing} {saving} {error} label={editing ? 'Save' : 'Create'} disabled={!name.trim()} leading={editing ? deleteButton : undefined} onsave={save} ondiscard={onclose} />
