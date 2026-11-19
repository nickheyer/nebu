<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import Text from '#lib/ui/fields/Text.svelte';
	import type { Option } from '#lib/ui/fields/types.js';
	
	let { value = $bindable(''), spans = [], onchange }: { value?: string; spans?: Option[]; onchange?: (v: string) => void } = $props();

	const scale: Record<string, number> = { k: 1e3, m: 1e6, b: 1e9 };

	let low = $state('');
	let high = $state('');
	// The ends follow a value set from outside
	$effect.pre(() => {
		[low = '', high = ''] = value.split('..');
	});

	const lowCount = $derived(count(low));
	const highCount = $derived(count(high));
	const lowError = $derived(Number.isNaN(lowCount) ? 'A count such as 500M' : undefined);
	const highError = $derived(Number.isNaN(highCount) ? 'A count such as 7B' : lowCount !== undefined && highCount !== undefined && highCount < lowCount ? 'Under the minimum' : undefined);

	// A typed end as a number: nothing when blank, not a number when it is no count
	function count(s: string): number | undefined {
		if (s.trim() === '') return undefined;
		const m = /^(\d+(?:\.\d+)?)\s*([kmb]?)$/i.exec(s.trim());
		return m ? Number(m[1]) * (scale[(m[2] ?? '').toLowerCase()] ?? 1) : NaN;
	}

	// Whether the ends stand where a span puts them, however the counts are spelled
	function spanned(span: string): boolean {
		const [from = '', to = ''] = span.split('..');
		return value !== '' && count(from) === lowCount && count(to) === highCount;
	}

	function set(next: string) {
		if (next === value) return;
		value = next;
		onchange?.(next);
	}

	// The range changes once both ends read as counts in order
	function commit() {
		if (lowError || highError) return;
		set(low.trim() || high.trim() ? `${low.trim()}..${high.trim()}` : '');
	}
</script>

<div class="range">
	{#if spans.length}
		<div class="spans dense" role="group" aria-label="Common ranges">
			{#each spans as s (s.value)}
				{@const on = spanned(s.value)}
				<NeoButton toggle checked={on} color={on ? 'primary' : undefined} aria-pressed={on} onclick={() => set(on ? '' : s.value)}>{s.label}</NeoButton>
			{/each}
		</div>
	{/if}
	<div class="ends">
		<Text bind:value={low} label="Minimum" placeholder="No min" error={lowError} onchange={commit} />
		<Text bind:value={high} label="Maximum" placeholder="No max" error={highError} onchange={commit} />
	</div>
</div>

<style>
	.range {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-4);
		min-width: 0;
	}
	.spans {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-2);
	}
	.spans :global(.neo-button) {
		white-space: nowrap;
	}
	.ends {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: var(--nebu-space-3);
		align-items: start;
		min-width: 0;
	}
</style>
