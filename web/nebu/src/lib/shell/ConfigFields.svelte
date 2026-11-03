<script lang="ts">
	import { NeoInput, NeoNativeSelect, NeoSwitch } from '#lib/neo/inputs/index.js';
	import { ConfigType, type ConfigField } from '#lib/proto/nebu/v1/source_pb.js';

	let { fields, values = $bindable({}) }: { fields: ConfigField[]; values?: Record<string, string> } = $props();

	$effect(() => {
		for (const f of fields) {
			if (values[f.name] === undefined && f.default) values[f.name] = f.default;
		}
	});

	function options(f: ConfigField) {
		return f.choices.map((c) => ({ value: c, label: f.choiceLabels[c] ?? c, disabled: c in f.choiceUnmet, title: f.choiceUnmet[c] }));
	}

	function flag(f: ConfigField): boolean {
		return values[f.name] === 'true';
	}

	function setFlag(f: ConfigField, on: boolean) {
		values[f.name] = on ? 'true' : 'false';
	}

	function inputType(f: ConfigField): 'text' | 'url' | 'number' {
		if (f.type === ConfigType.URL) return 'url';
		if (f.type === ConfigType.INT) return 'number';
		return 'text';
	}
</script>

{#each fields as f (f.name)}
	{#if f.choices.length}
		<NeoNativeSelect bind:value={values[f.name]} options={options(f)} label={f.label || f.name} message={f.description} required={f.required} />
	{:else if f.type === ConfigType.BOOL}
		<NeoSwitch checked={flag(f)} onchange={(e) => setFlag(f, (e.currentTarget as HTMLInputElement).checked)} label={f.label || f.name} message={f.description} />
	{:else}
		<NeoInput
			bind:value={values[f.name]}
			type={inputType(f)}
			label={f.label || f.name}
			message={f.description}
			placeholder={f.placeholder}
			required={f.required}
			autocomplete="off"
		/>
	{/if}
{/each}
