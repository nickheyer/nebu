<script lang="ts">
	import { untrack } from 'svelte';
	import JsonTree from '#lib/ui/JsonTree.svelte';
	import { num } from '#lib/format.js';

	// Characters of a string shown before the rest folds behind a count
	const stringCut = 1000;

	// A JSON value as a tree, containers past the open depth folded and long strings cut until asked for
	let { value, name, depth = 2, last = true }: { value: unknown; name?: string; depth?: number; last?: boolean } = $props();

	const container = $derived(value !== null && typeof value === 'object');
	const entries = $derived<[string | undefined, unknown][]>(
		Array.isArray(value) ? value.map((v) => [undefined, v]) : container ? Object.entries(value as Record<string, unknown>) : []
	);
	const brackets = $derived(Array.isArray(value) ? '[]' : '{}');
	let open = $state(untrack(() => depth > 0));
	let whole = $state(false);
	const text = $derived(typeof value === 'string' ? JSON.stringify(value) : String(value));
	const shown = $derived(whole || text.length <= stringCut ? text : text.slice(0, stringCut));
</script>

<div class="node">
	{#if container}
		<button type="button" class="line toggle" aria-expanded={open} onclick={() => (open = !open)}>
			<span class="caret dim" class:open>▸</span>{#if name !== undefined}<span class="key">{JSON.stringify(name)}</span><span class="dim">: </span>{/if}<span class="dim">{brackets[0]}</span>{#if !open || entries.length === 0}<span class="dim">{entries.length ? ` ${num(entries.length)} ` : ''}{brackets[1]}{last ? '' : ','}</span>{/if}
		</button>
		{#if open && entries.length}
			<div class="children">
				{#each entries as [k, v], i (i)}
					<JsonTree value={v} name={k} depth={depth - 1} last={i === entries.length - 1} />
				{/each}
			</div>
			<div class="line dim close">{brackets[1]}{last ? '' : ','}</div>
		{/if}
	{:else}
		<div class="line leaf">
			{#if name !== undefined}<span class="key">{JSON.stringify(name)}</span><span class="dim">: </span>{/if}<span class="value">{shown}</span>{#if shown.length < text.length}<button type="button" class="more dim" onclick={() => (whole = true)}>{num(text.length - shown.length)} more</button>{/if}<span class="dim">{last ? '' : ','}</span>
		</div>
	{/if}
</div>

<style>
	.node {
		min-width: 0;
		font-family: var(--nebu-font-mono);
		font-size: 13px;
		line-height: 1.5;
	}
	.line {
		display: block;
		width: 100%;
		min-width: 0;
		padding: 0;
		font: inherit;
		color: inherit;
		text-align: left;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		background: none;
		border: 0;
	}
	.toggle {
		cursor: pointer;
	}
	.leaf,
	.close {
		padding-left: 1.5ch;
	}
	.caret {
		display: inline-block;
		width: 1.5ch;
		transition: transform var(--nebu-normal) var(--nebu-settle);
	}
	.caret.open {
		transform: rotate(90deg);
	}
	.key {
		font-weight: 500;
	}
	.children {
		padding-left: 2ch;
	}
	.more {
		padding: 0 1ch;
		font: inherit;
		cursor: pointer;
		background: none;
		border: 0;
		text-decoration: underline;
	}
</style>
