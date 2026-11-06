<script lang="ts">
	import { ConfigType, type ConfigField } from '#lib/proto/nebu/v1/source_pb.js';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Path from '#lib/ui/fields/Path.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';

	let { fields, values = $bindable({}), secretsSet = false, disabled = false }: { fields: ConfigField[]; values?: Record<string, string>; secretsSet?: boolean; disabled?: boolean } = $props();

	$effect(() => {
		for (const f of fields) {
			if (values[f.name] === undefined && f.default) values[f.name] = f.default;
		}
	});

	// Choices with their labels, the unmet ones disabled with the reason under them
	function options(f: ConfigField) {
		return f.choices.map((c) => ({ value: c, label: f.choiceLabels[c] ?? c, disabled: c in f.choiceUnmet, description: f.choiceUnmet[c] }));
	}

	function pairs(list: ConfigField[]): ConfigField[][] {
		const out: ConfigField[][] = [];
		for (let i = 0; i < list.length; i += 2) out.push(list.slice(i, i + 2));
		return out;
	}
</script>

{#each pairs(fields) as pair, i (i)}
	<FieldRow>
		{#each pair as f (f.name)}
			{#if f.choices.length}
				<Select value={values[f.name] || undefined} options={options(f)} label={f.label || f.name} info={f.description || undefined} required={f.required} {disabled} onchange={(v) => (values[f.name] = v ?? '')} />
			{:else if f.type === ConfigType.BOOL}
				<Switch checked={values[f.name] === 'true'} label={f.label || f.name} info={f.description || undefined} {disabled} onchange={(on) => (values[f.name] = on ? 'true' : 'false')} />
			{:else if f.type === ConfigType.INT}
				<NumberField value={Number(values[f.name] ?? 0)} label={f.label || f.name} info={f.description || undefined} placeholder={f.placeholder} {disabled} onchange={(n) => (values[f.name] = String(n))} />
			{:else if f.type === ConfigType.PATH}
				<Path value={values[f.name] ?? ''} label={f.label || f.name} info={f.description || undefined} placeholder={f.placeholder} {disabled} onchange={(v) => (values[f.name] = v)} />
			{:else if f.type === ConfigType.ENV}
				<Secret bind:value={values[f.name]} label={f.label || f.name} info={f.description || undefined} placeholder={f.placeholder || f.name.toUpperCase()} set={secretsSet} {disabled} />
			{:else}
				<Text value={values[f.name] ?? ''} label={f.label || f.name} info={f.description || undefined} placeholder={f.placeholder} type={f.type === ConfigType.URL ? 'url' : 'text'} max={256} required={f.required} {disabled} onchange={(v) => (values[f.name] = v)} />
			{/if}
		{/each}
	</FieldRow>
{/each}
