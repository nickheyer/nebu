<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';

	let { text, action, href, onclick }: { text: string; action?: string; href?: string; onclick?: () => void } = $props();
</script>

<div class="empty">
	<p class="muted">{text}</p>
	{#if action}
		<NeoButton elevation={2} {href} {onclick}>{action}</NeoButton>
	{/if}
</div>

<style>
	.empty {
		display: flex;
		flex-direction: column;
		gap: 12px;
		align-items: center;
		padding: 32px 16px;
		text-align: center;
	}
</style>
