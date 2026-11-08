<script lang="ts">
	import Tip from '#lib/ui/Tip.svelte';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import { shortId } from '#lib/format.js';

	export type StrKind = 'name' | 'repo' | 'address' | 'id' | 'version' | 'message' | 'path' | 'text';

	let {
		value,
		kind = 'name',
		width,
		tip,
		weight = 400,
		color,
		left: fromLeft
	}: { value: string; kind?: StrKind; width?: string; tip?: string; weight?: 400 | 500 | 600; color?: 'muted' | 'error' | 'warning' | 'success'; left?: boolean } = $props();

	const widths: Record<StrKind, string> = { name: '24ch', repo: '40ch', address: '24ch', id: '8ch', version: '12ch', message: '64ch', path: '40ch', text: '40ch' };
	const mono = $derived(kind === 'id' || kind === 'address' || kind === 'version' || kind === 'path');
	const shown = $derived(kind === 'id' ? shortId(value) : value);
	const left = $derived(fromLeft ?? (kind === 'repo' || kind === 'path'));
	const full = $derived(tip ?? (kind === 'id' ? value : shown !== value || kind === 'message' || (value?.length ?? 0) > 20 ? value : undefined));
	// Left to right marks keep a left ellipsis run in reading order inside the right to left box
	const text = $derived(left ? `\u200e${shown}\u200e` : shown);
</script>

<Tip text={full} tag="span" class="str-tip">
	<span class="str ellipsis" class:mono class:left class:muted={color === 'muted'} class:error-text={color === 'error'} class:warning-text={color === 'warning'} class:success-text={color === 'success'} style:max-width={width ?? widths[kind]} style:font-weight={weight}>{text}</span>
	{#if kind === 'id' && value}<CopyButton text={value} />{/if}
</Tip>

<style>
	:global(.str-tip) {
		align-items: center;
		gap: 2px;
	}
	.str {
		display: inline-block;
		max-width: 100%;
		vertical-align: bottom;
	}
	.left {
		direction: rtl;
		text-align: left;
	}
</style>
