<script lang="ts" generics="V extends string | number">
	import { NeoCheckbox } from '#lib/neo/inputs/index.js';
	import Tip from '#lib/ui/Tip.svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import type { Option } from '#lib/ui/fields/types.js';

	let {
		value = $bindable([]),
		options,
		label,
		disabled = false,
		empty = '—'
	}: { value?: V[]; options: Option<V>[]; label?: string; disabled?: boolean; empty?: string } = $props();

	function toggle(v: V, on: boolean) {
		const set = new Set(value);
		if (on) set.add(v);
		else set.delete(v);
		value = options.map((o) => o.value).filter((x) => set.has(x));
	}
</script>

<div class="field">
	<FieldLabel {label} />
	{#if options.length === 0}
		<span class="secondary">{empty}</span>
	{:else}
		<div class="list">
			{#each options as o, i (String(o.value))}
				{#snippet line()}
					<span class="line">
						<span class="text ellipsis">{o.label}</span>
						{#if o.description}<span class="aside ellipsis">{o.description}</span>{/if}
					</span>
				{/snippet}
				<Tip text={o.tip} tag="div" class="checkline">
					<span class="row" style:--i={i}>
						<NeoCheckbox
							checked={value.includes(o.value)}
							disabled={disabled || o.disabled}
							label={line}
							elevation={-1}
							onchange={(e) => toggle(o.value, (e.currentTarget as HTMLInputElement).checked)}
						/>
					</span>
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
	:global(.checkline) {
		display: flex;
		min-width: 0;
	}
	.row {
		display: flex;
		flex: 1 1 auto;
		min-width: 0;
		animation: nebu-reveal 220ms var(--nebu-settle) both;
		animation-delay: calc(var(--i) * 20ms);
	}
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
