<script lang="ts">
	import NeoLabel from '#lib/neo/inputs/common/NeoLabel.svelte';
	import InfoTip from '#lib/ui/InfoTip.svelte';

	let { label, info, forId }: { label?: string; info?: string; forId?: string } = $props();
</script>

{#if label}
	<span class="fieldlabel">
		<NeoLabel {label} for={forId} />
		{#if info}<InfoTip text={info} />{/if}
	</span>
{/if}

<style>
	.fieldlabel {
		display: inline-flex;
		gap: 6px;
		align-items: center;
	}
</style>
