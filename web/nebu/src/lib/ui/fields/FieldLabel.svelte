<script lang="ts">
	import NeoLabel from '#lib/neo/inputs/common/NeoLabel.svelte';

	let { label, forId }: { label?: string; forId?: string } = $props();
</script>

{#if label}
	<span class="fieldlabel">
		<NeoLabel {label} for={forId} />
	</span>
{/if}

<style>
	.fieldlabel {
		display: inline-flex;
		align-items: center;
	}
</style>
