<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { untrack } from 'svelte';
	import { reason, rpc } from '#lib/api/transport.js';
	import { enumLabel } from '#lib/format.js';
	import { SourceKind, SourceKindSchema, SourceSchema, type ConfigField, type Provider, type SourceStatus } from '#lib/proto/nebu/v1/source_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Str from '#lib/ui/Str.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import ConfigFields from '#lib/pages/runtimes/ConfigFields.svelte';

	let { status, providers, onclose }: { status?: SourceStatus; providers: Provider[]; onclose: () => void } = $props();

	const draft = untrack(() => status?.source);
	const editing = draft !== undefined;
	let kind = $state<SourceKind>(untrack(() => draft?.kind ?? providers[0]?.kind ?? SourceKind.UNSPECIFIED));
	let id = $state(draft?.id ?? '');
	let name = $state(draft?.name ?? '');
	let saving = $state(false);
	let error = $state<string | undefined>(undefined);

	const provider = $derived(providers.find((p) => p.kind === kind));
	const fields = $derived(status?.capabilities?.fields.length ? status.capabilities.fields : (provider?.fields ?? []));
	let config = $state<Record<string, string>>(untrack(() => withDefaults(fields, draft?.config ?? {})));
	const providerOptions = $derived.by(() => {
		const list = providers.map((p) => ({ value: p.kind, label: p.name, description: p.description }));
		if (!list.some((o) => o.value === kind)) list.push({ value: kind, label: status?.capabilities?.name || enumLabel(SourceKindSchema, kind), description: '' });
		return list;
	});

	const baseline = untrack(() => snapshot());

	// A new source opened before the providers listed takes the first one once they arrive
	$effect(() => {
		const first = providers[0];
		if (!editing && kind === SourceKind.UNSPECIFIED && first) untrack(() => pickProvider(first.kind));
	});
	const dirty = $derived(!editing || snapshot() !== baseline);

	// Stored values over each field's default
	function withDefaults(list: ConfigField[], stored: Record<string, string>): Record<string, string> {
		const out: Record<string, string> = {};
		for (const f of list) if (f.default) out[f.name] = f.default;
		return { ...out, ...stored };
	}

	// The form as one comparable string, empty values counting as unset
	function snapshot(): string {
		const set = Object.entries(config)
			.filter(([, v]) => v !== '' && v !== undefined)
			.sort(([a], [b]) => a.localeCompare(b));
		return JSON.stringify([kind, id.trim(), name.trim(), set]);
	}

	function pickProvider(k: SourceKind | undefined) {
		const p = providers.find((x) => x.kind === k);
		if (!p) return;
		kind = p.kind;
		config = withDefaults(p.fields, {});
	}

	function discard() {
		if (!draft) {
			onclose();
			return;
		}
		kind = draft.kind;
		id = draft.id;
		name = draft.name;
		config = withDefaults(fields, draft.config);
	}

	async function save() {
		saving = true;
		error = undefined;
		try {
			const source = create(SourceSchema, { id: id.trim(), kind, name: name.trim() || id.trim(), config: { ...config } });
			if (editing) await rpc.sources.updateSource({ source });
			else await rpc.sources.createSource({ source });
			toast.success(`Saved ${source.id}`);
			onclose();
		} catch (err) {
			error = reason(err);
		} finally {
			saving = false;
		}
	}
</script>

<PanelHeader title={draft ? draft.name || draft.id : 'New source'} subtitle={draft && provider?.name !== (draft.name || draft.id) ? provider?.name : undefined} {onclose} />
<Stack gap={24}>
	{#if status?.error}<Str value={status.error} kind="message" color="error" width="100%" />{/if}
	<FormSection title="Source">
		<FieldRow>
			<Select value={kind} options={providerOptions} label="Provider" disabled={editing} onchange={pickProvider} />
		</FieldRow>
		<FieldRow>
			<Text bind:value={id} label="Id" info="Used in URLs, fixed once created" readonly={editing} mono required />
			<Text bind:value={name} label="Name" placeholder={id} />
		</FieldRow>
	</FormSection>
	{#if fields.length}
		<FormSection title="Configuration">
			<ConfigFields {fields} bind:values={config} />
		</FormSection>
	{/if}
</Stack>
<SaveBar {dirty} always={!editing} {saving} {error} label={editing ? 'Save' : 'Create'} disabled={!id.trim() || kind === SourceKind.UNSPECIFIED} onsave={save} ondiscard={discard} />

