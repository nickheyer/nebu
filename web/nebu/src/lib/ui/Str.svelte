<script lang="ts">
	import Tip from '#lib/ui/Tip.svelte';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import { shortId } from '#lib/format.js';

	export type StrKind = 'name' | 'repo' | 'address' | 'id' | 'version' | 'message' | 'path' | 'text';

	// A string of a known kind that wraps in a tight cell, or clips to one line with the rest on hover
	let {
		value,
		kind = 'name',
		width,
		tip,
		weight = 400,
		color,
		clip = false
	}: { value: string; kind?: StrKind; width?: string; tip?: string; weight?: 300 | 400 | 500; color?: 'muted' | 'error' | 'warning' | 'success'; clip?: boolean } = $props();

	const mono = $derived(kind === 'id' || kind === 'address' || kind === 'version' || kind === 'path');
	const shown = $derived(kind === 'id' ? shortId(value) : value);
	const hint = $derived(tip ?? (kind === 'id' ? value : undefined));
	let el = $state<HTMLElement>();
	let cut = $state(false);

	// Follows whether the one line holds less than the whole string, as the cell resizes
	function measure(node: HTMLElement) {
		if (!clip) return;
		el = node;
		const ro = new ResizeObserver(() => (cut = node.scrollWidth > node.clientWidth));
		ro.observe(node);
		return () => ro.disconnect();
	}

	$effect(() => {
		void shown;
		if (el) cut = el.scrollWidth > el.clientWidth;
	});
	const full = $derived(cut ? (tip ?? value) : hint === shown ? undefined : hint);
</script>

<Tip text={full} tag="span" class="str-tip">
	<span class="str" class:clip {@attach measure} class:mono class:muted={color === 'muted'} class:error-text={color === 'error'} class:warning-text={color === 'warning'} class:success-text={color === 'success'} style:max-width={width} style:font-weight={weight}>{shown}</span>
	{#if kind === 'id' && value}<CopyButton text={value} />{/if}
</Tip>

<style>
	:global(.str-tip) {
		align-items: center;
		gap: 2px;
	}
	.str {
		display: inline-block;
		min-width: 0;
		max-width: 100%;
		vertical-align: bottom;
		overflow-wrap: anywhere;
	}
	.clip {
		display: block;
		overflow: hidden;
		white-space: nowrap;
		text-overflow: ellipsis;
	}
</style>
