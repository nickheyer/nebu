<script lang="ts">
	import type { Timestamp } from '@bufbuild/protobuf/wkt';
	import { ago, when } from '#lib/format.js';
	import Tip from '#lib/ui/Tip.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';

	let { value, muted = true }: { value: Timestamp | Date | undefined; muted?: boolean } = $props();
	const text = $derived(ago(value, clockTick.now));
</script>

{#if value}
	<Tip text={when(value)} tag="span">
		<span class="time" class:muted>{text}</span>
	</Tip>
{/if}

<style>
	.time {
		display: inline-block;
		min-width: 9ch;
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
</style>
