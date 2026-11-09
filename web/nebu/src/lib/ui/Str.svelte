<script lang="ts">
	import Tip from '#lib/ui/Tip.svelte';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import { shortId } from '#lib/format.js';

	export type StrKind = 'name' | 'repo' | 'address' | 'id' | 'version' | 'message' | 'path' | 'text';

	// A string of a known kind, wrapping when its cell runs out of room rather than cutting off
	let {
		value,
		kind = 'name',
		width,
		tip,
		weight = 400,
		color
	}: { value: string; kind?: StrKind; width?: string; tip?: string; weight?: 300 | 400 | 500; color?: 'muted' | 'error' | 'warning' | 'success' } = $props();

	const mono = $derived(kind === 'id' || kind === 'address' || kind === 'version' || kind === 'path');
	const shown = $derived(kind === 'id' ? shortId(value) : value);
	const full = $derived(tip ?? (kind === 'id' ? value : undefined));
</script>

<Tip text={full} tag="span" class="str-tip">
	<span class="str" class:mono class:muted={color === 'muted'} class:error-text={color === 'error'} class:warning-text={color === 'warning'} class:success-text={color === 'success'} style:max-width={width} style:font-weight={weight}>{shown}</span>
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
</style>
