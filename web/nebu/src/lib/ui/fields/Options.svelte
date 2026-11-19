<script lang="ts" generics="V extends string | number">
	import { NeoInput } from '#lib/neo/inputs/index.js';
	import Tip from '#lib/ui/Tip.svelte';
	import type { Option } from '#lib/ui/fields/types.js';
	import MagnifyingGlassIcon from 'phosphor-svelte/lib/MagnifyingGlassIcon';

	let {
		value = $bindable([]),
		options,
		label,
		search = options.length > 8,
		onchange
	}: { value?: V[]; options: Option<V>[]; label: string; search?: boolean; onchange?: (v: V[]) => void } = $props();

	type Row = { head: string } | { option: Option<V> };

	let typed = $state('');
	let list = $state<HTMLElement>();

	const chosen = $derived(new Set(value));
	const words = $derived(typed.trim().toLowerCase().split(/\s+/).filter(Boolean));
	const found = $derived(words.length ? options.filter((o) => words.every((w) => `${o.label}\n${o.value}\n${o.group ?? ''}`.toLowerCase().includes(w))) : options);
	const rows = $derived.by((): Row[] => {
		if (!found.some((o) => o.group)) return found.map((option) => ({ option }));
		const groups = new Map<string, Option<V>[]>();
		for (const o of found) groups.set(o.group ?? '', [...(groups.get(o.group ?? '') ?? []), o]);
		return [...groups].flatMap(([head, all]): Row[] => [...(head ? [{ head }] : []), ...all.map((option) => ({ option }))]);
	});

	function toggle(v: V) {
		const set = new Set(value);
		if (set.has(v)) set.delete(v);
		else set.add(v);
		value = options.map((o) => o.value).filter((x) => set.has(x));
		onchange?.(value);
	}

	// The arrow keys walk the rows, down from the search box into them and up from the first back to it
	function onkeydown(e: KeyboardEvent) {
		if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
		const all = [...(list?.querySelectorAll<HTMLElement>('.option:not(:disabled)') ?? [])];
		const at = all.indexOf(document.activeElement as HTMLElement);
		const next = e.key === 'ArrowDown' ? all[at + 1] : at > 0 ? all[at - 1] : at === 0 ? list?.parentElement?.querySelector<HTMLElement>('input') : undefined;
		if (!next) return;
		e.preventDefault();
		next.focus();
	}
</script>

{#snippet line(o: Option<V>)}
	<button type="button" class="option" role="option" aria-selected={chosen.has(o.value)} disabled={o.disabled} onclick={() => toggle(o.value)}>
		<span class="box" aria-hidden="true"></span>
		<span class="name">{o.label}</span>
	</button>
{/snippet}

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="options" {onkeydown}>
	{#if search}
		<div class="find dense">
			<NeoInput bind:value={typed} elevation={-1} floating={false} clearable placeholder="Search" aria-label={`Search ${label}`}>
				{#snippet before()}<MagnifyingGlassIcon size={16} />{/snippet}
			</NeoInput>
		</div>
	{/if}
	<div class="rows" role="listbox" aria-label={label} aria-multiselectable="true" bind:this={list}>
		{#each rows as r ('head' in r ? `\n${r.head}` : String(r.option.value))}
			{#if 'head' in r}
				<div class="head" role="presentation">{r.head}</div>
			{:else if r.option.tip}
				<Tip text={r.option.tip} placement="left" tag="div" class="optionline">{@render line(r.option)}</Tip>
			{:else}
				{@render line(r.option)}
			{/if}
		{:else}
			<div class="none secondary">Nothing matches {typed.trim()}</div>
		{/each}
	</div>
</div>

<style>
	.options {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
		min-height: 0;
	}
	.find {
		display: flex;
		flex: 0 0 auto;
	}
	.find :global(.neo-input-group) {
		flex: 1 1 auto;
		min-width: 0;
	}
	/* The rows scroll under the search box, their bar clear of the ticks */
	.rows {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		gap: 1px;
		min-height: 0;
		margin: 0 calc(-1 * var(--nebu-space-2));
		padding: 0 var(--nebu-space-2);
		overflow-y: auto;
		overscroll-behavior: contain;
	}
	:global(.optionline) {
		display: flex;
		flex: 0 0 auto;
	}
	.option {
		display: flex;
		flex: 0 0 auto;
		gap: 10px;
		align-items: flex-start;
		width: 100%;
		min-width: 0;
		padding: 6px var(--nebu-space-2);
		font: inherit;
		color: inherit;
		text-align: left;
		cursor: pointer;
		background: none;
		border: 0;
		border-radius: 6px;
		appearance: none;
	}
	.option:hover {
		background: var(--neo-background-color-hover);
	}
	.option:focus-visible {
		outline: 2px solid var(--neo-color-primary);
		outline-offset: -2px;
	}
	.option:disabled {
		color: var(--nebu-disabled-text);
		cursor: default;
		background: none;
	}
	.box {
		position: relative;
		flex: 0 0 auto;
		width: 16px;
		height: 16px;
		margin-top: calc((1lh - 16px) / 2);
		border-radius: 4px;
		box-shadow: var(--neo-box-shadow-pressed-2);
	}
	.option[aria-selected='true'] .box {
		background: var(--neo-color-primary);
		box-shadow: none;
	}
	/* The tick is the right and bottom edges of a 4 by 8 box, edges counted in that size, turned 45 degrees about its
	   centre, which sits on the centre of the square. The two strokes lie half a pixel toward the corner they share,
	   so the turned box moves back by that much and the middle of the tick lands on the middle of the square. */
	.option[aria-selected='true'] .box::after {
		position: absolute;
		top: 4px;
		left: 6px;
		box-sizing: border-box;
		width: 4px;
		height: 8px;
		content: '';
		border: solid var(--neo-background-color);
		border-width: 0 2px 2px 0;
		transform: rotate(45deg) translate(-0.5px, -0.5px);
	}
	.name {
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.option[aria-selected='true'] .name {
		color: var(--neo-color-primary);
	}
	.head {
		flex: 0 0 auto;
		padding: var(--nebu-space-3) var(--nebu-space-2) var(--nebu-space-1);
		font-size: var(--nebu-text-caption);
		color: var(--nebu-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.06em;
	}
	.head:first-child {
		padding-top: var(--nebu-space-1);
	}
	.none {
		padding: var(--nebu-space-3) var(--nebu-space-2);
	}
</style>
