<script lang="ts">
	import { NeoSwitch } from '#lib/neo/inputs/index.js';
	import InfoTip from '#lib/ui/InfoTip.svelte';

	let {
		checked = $bindable(false),
		label,
		info,
		disabled = false,
		message,
		onchange
	}: { checked?: boolean; label?: string; info?: string; disabled?: boolean; message?: string; onchange?: (on: boolean) => void } = $props();
</script>

<div class="switch">
	<NeoSwitch bind:checked {label} {disabled} {message} elevation={-1} onchange={(e) => onchange?.((e.currentTarget as HTMLInputElement).checked)} />
	{#if info}<InfoTip text={info} />{/if}
</div>

<style>
	.switch {
		display: inline-flex;
		gap: 6px;
		align-items: center;
		min-width: 0;
	}
</style>
