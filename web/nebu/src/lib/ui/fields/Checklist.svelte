<script lang="ts" generics="V extends string | number">
	import { NeoCheckbox } from '#lib/neo/inputs/index.js';
	import Tip from '#lib/ui/Tip.svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import type { Option } from '#lib/ui/fields/types.js';

	let {
		value = $bindable([]),
		options,
		label,
		info,
		disabled = false,
		empty = 'Nothing to choose from'
	}: { value?: V[]; options: Option<V>[]; label?: string; info?: string; disabled?: boolean; empty?: string } = $props();

	function toggle(v: V, on: boolean) {
		const set = new Set(value);
		if (on) set.add(v);
		else set.delete(v);
		value = options.map((o) => o.value).filter((x) => set.has(x));
	}
</script>

<div class="field">
	<FieldLabel {label} {info} />
	{#if options.length === 0}
		<span class="secondary">{empty}</span>
	{:else}
		<div class="list">
			{#each options as o (String(o.value))}
				{#snippet line()}
					<span class="line">
						<span class="text ellipsis">{o.label}</span>
						{#if o.description}<span class="aside ellipsis">{o.description}</span>{/if}
					</span>
				{/snippet}
				<Tip text={o.tip} tag="div">
					<NeoCheckbox
						checked={value.includes(o.value)}
						disabled={disabled || o.disabled}
						label={line}
						elevation={-1}
						onchange={(e) => toggle(o.value, (e.currentTarget as HTMLInputElement).checked)}
					/>
				</Tip>
			{/each}
		</div>
	{/if}
</div>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}
	.list {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}
	/* The secondary word sits on the label line, muted, never on a line of its own */
	.line {
		display: inline-flex;
		gap: var(--nebu-space-2);
		align-items: baseline;
		min-width: 0;
		max-width: 100%;
	}
	.text {
		min-width: 0;
	}
	.aside {
		min-width: 0;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
	}
</style>
