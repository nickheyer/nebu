<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { untrack } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { plural } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { SchedulerConfigSchema, SchedulerConfigSpecSchema, type SchedulerConfig } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Conditions from '#lib/ui/Conditions.svelte';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Code from '#lib/ui/fields/Code.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { schedulerSkeleton } from '#lib/pages/gateway/gateway.js';

	let { config, references, onclose }: { config?: SchedulerConfig; references: number; onclose: () => void } = $props();

	const start = untrack(() => config);
	const editing = start !== undefined;
	let name = $state(start?.name ?? '');
	let yaml = $state(start?.spec?.yaml ?? schedulerSkeleton);
	let valid = $state(true);
	let codeError = $state<string | undefined>(undefined);
	let validating = $state(false);
	let saving = $state(false);
	let error = $state<string | undefined>(undefined);
	let height = $state(800);

	const current = $derived(editing ? live.schedulerConfigs.get(name) : undefined);
	const dirty = $derived(!editing || yaml !== start?.spec?.yaml);
	// Lines that fill the panel below its header and above the save bar, a line being 19.5px
	const rows = $derived(Math.max(12, Math.floor((height - (editing ? 330 : 420)) / 19.5)));
	const complete = $derived(name.trim() !== '' && valid);

	function draft() {
		return create(SchedulerConfigSchema, { name: name.trim(), spec: create(SchedulerConfigSpecSchema, { yaml }) });
	}

	async function validate() {
		validating = true;
		try {
			const res = await rpc.gateway.putSchedulerConfig({ config: draft(), validateOnly: true });
			codeError = undefined;
			toast.success('Configuration loads', res.config?.status?.conditions[0]?.message);
		} catch (err) {
			codeError = reason(err);
			toast.error('Configuration does not load', codeError);
		} finally {
			validating = false;
		}
	}

	async function save() {
		saving = true;
		error = undefined;
		try {
			await rpc.gateway.putSchedulerConfig({ config: draft() });
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
			await rpc.gateway.deleteSchedulerConfig({ name });
			toast.success(`Deleted ${name}`);
			onclose();
		} catch (err) {
			toast.error('Delete scheduler failed', reason(err));
		}
	}
</script>

<svelte:window bind:innerHeight={height} />

<PanelHeader title={editing ? name : 'New scheduler'} subtitle={editing ? plural(references, 'pool') : undefined} {onclose}>
	{#snippet tools()}
		<Row gap={8} wrap={false}>
			<NeoButton elevation={2} loading={validating} disabled={!complete} onclick={validate}>Validate</NeoButton>
			{#if editing && references === 0}<Confirm label="Delete" question="Delete {name}?" danger onconfirm={remove} />{/if}
		</Row>
	{/snippet}
</PanelHeader>
<Stack gap={24}>
	<FormSection title="Scheduler">
		{#if !editing}
			<FieldRow>
				<Text bind:value={name} label="Name" required max={64} />
				<span></span>
			</FieldRow>
		{/if}
		<Code bind:value={yaml} language="yaml" bind:valid bind:error={codeError} {rows} label="Configuration" />
	</FormSection>
	{#if current}
		<Section title="Conditions" level={3}>
			<Conditions conditions={current.status?.conditions} />
		</Section>
	{/if}
</Stack>
<SaveBar {dirty} always={!editing} {saving} {error} label={editing ? 'Save' : 'Create'} disabled={!complete} onsave={save} ondiscard={onclose} />
