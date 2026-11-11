<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { untrack } from 'svelte';
	import { reason, rpc } from '#lib/api/transport.js';
	import { plural } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { SchedulerConfigSchema, SchedulerConfigSpecSchema, type SchedulerConfig } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Conditions from '#lib/ui/Conditions.svelte';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Code from '#lib/ui/fields/Code.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import { schedulerSkeleton } from '#lib/pages/gateway/gateway.js';

	let { config, references, onclose }: { config?: SchedulerConfig; references: number; onclose: () => void } = $props();

	const start = untrack(() => config);
	const editing = start !== undefined;
	let name = $state(start?.name ?? '');
	let yaml = $state(start?.spec?.yaml ?? schedulerSkeleton);
	let valid = $state(true);
	let codeError = $state<string | undefined>(undefined);
	let saving = $state(false);
	let error = $state<string | undefined>(undefined);

	const current = $derived(editing ? live.schedulerConfigs.get(name) : undefined);
	const dirty = $derived(!editing || yaml !== start?.spec?.yaml);
	const complete = $derived(name.trim() !== '' && valid);

	function draft() {
		return create(SchedulerConfigSchema, { name: name.trim(), spec: create(SchedulerConfigSpecSchema, { yaml }) });
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

<div class="top">
	<PanelHeader title={editing ? name : 'New scheduler'} subtitle={editing ? plural(references, 'pool') : undefined} {onclose} />
</div>
<div class="body">
	<div class="form">
		{#if !editing}
			<FieldRow>
				<Text bind:value={name} label="Name" required max={64} />
				<span></span>
			</FieldRow>
		{/if}
		<Code bind:value={yaml} language="yaml" bind:valid bind:error={codeError} fill rows={12} label="Configuration" />
	</div>
	{#if current?.status?.conditions.length}
		<Section title="Status" level={3}>
			<Conditions conditions={current.status?.conditions} />
		</Section>
	{/if}
</div>
{#snippet deleteButton()}
	<Confirm label="Delete" question="Delete {name}?" danger placement="top-start" onconfirm={remove} />
{/snippet}

<SaveBar {dirty} always={!editing} {saving} {error} label={editing ? 'Save' : 'Create'} disabled={!complete} leading={editing && references === 0 ? deleteButton : undefined} onsave={save} ondiscard={onclose} />

<style>
	.top {
		flex: none;
	}
	/* The editor takes the height the sheet has under the header, so both columns pass it down */
	.body,
	.form {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		min-width: 0;
		min-height: 0;
	}
	.body {
		gap: 24px;
	}
	.form {
		gap: 12px;
	}
</style>
