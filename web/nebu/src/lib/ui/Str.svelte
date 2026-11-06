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
		color
	}: { value: string; kind?: StrKind; width?: string; tip?: string; weight?: 400 | 500 | 600; color?: 'muted' | 'error' | 'warning' | 'success' } = $props();

	const widths: Record<StrKind, string> = { name: '24ch', repo: '40ch', address: '24ch', id: '8ch', version: '12ch', message: '64ch', path: '40ch', text: '40ch' };
	const mono = $derived(kind === 'id' || kind === 'address' || kind === 'version' || kind === 'path');
	const shown = $derived(kind === 'id' ? shortId(value) : value);
	const left = $derived(kind === 'repo' || kind === 'path');
	const full = $derived(tip ?? (kind === 'id' ? value : shown !== value || kind === 'message' || (value?.length ?? 0) > 20 ? value : undefined));
</script>

<Tip text={full} tag="span" class="str-tip">
	<span class="str ellipsis" class:mono class:left class:muted={color === 'muted'} class:error-text={color === 'error'} class:warning-text={color === 'warning'} class:success-text={color === 'success'} style:max-width={width ?? widths[kind]} style:font-weight={weight}>{shown}</span>
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
		unicode-bidi: plaintext;
	}
</style>
