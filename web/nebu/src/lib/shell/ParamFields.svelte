<script lang="ts">
	import { NeoInput, NeoNativeSelect, NeoSwitch } from '#lib/neo/inputs/index.js';
	import { ParamType, type Param } from '#lib/proto/nebu/v1/runtime_pb.js';

	let { params, values = $bindable({}), advanced = false }: { params: Param[]; values?: Record<string, string>; advanced?: boolean } = $props();

	const shown = $derived(params.filter((p) => advanced || (!p.advanced && !p.solved)));

	function label(p: Param): string {
		return (p.label || p.name) + (p.unit ? ` (${p.unit})` : '');
	}

	function hint(p: Param): string {
		const parts = [p.description];
		if (p.solved) parts.push('chosen by the planner when empty');
		else if (p.default) parts.push(`default ${p.default}`);
		return parts.filter(Boolean).join('. ');
	}

	function flag(p: Param): boolean {
		return (values[p.name] ?? p.default) === 'true';
	}

	function setFlag(p: Param, on: boolean) {
		values[p.name] = on ? 'true' : 'false';
	}

	function numeric(p: Param): boolean {
		return p.type === ParamType.INT || p.type === ParamType.FLOAT;
	}
</script>

{#each shown as p (p.name)}
	{#if p.choices.length}
		<NeoNativeSelect bind:value={values[p.name]} options={['', ...p.choices]} label={label(p)} message={hint(p)} />
	{:else if p.type === ParamType.BOOL}
		<NeoSwitch checked={flag(p)} onchange={(e) => setFlag(p, (e.currentTarget as HTMLInputElement).checked)} label={label(p)} message={hint(p)} />
	{:else}
		<NeoInput
			bind:value={values[p.name]}
			type={numeric(p) ? 'number' : 'text'}
			min={numeric(p) && p.min !== p.max ? p.min : undefined}
			max={numeric(p) && p.min !== p.max ? p.max : undefined}
			step={p.type === ParamType.FLOAT ? p.step || 'any' : p.step || undefined}
			label={label(p)}
			message={hint(p)}
			placeholder={p.solved ? 'auto' : p.default}
			autocomplete="off"
		/>
	{/if}
{/each}
