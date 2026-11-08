<script lang="ts">
	import type { Snippet } from 'svelte';
	import Collapse from '#lib/ui/Collapse.svelte';
	import InfoTip from '#lib/ui/InfoTip.svelte';

	let { title, info, children, advanced }: { title?: string; info?: string; children: Snippet; advanced?: Snippet } = $props();
	let open = $state(false);
</script>

<section class="form-section">
	{#if title}
		<div class="head">
			<h2>{title}</h2>
			{#if info}<InfoTip text={info} />{/if}
		</div>
	{/if}
	<div class="fields">
		{@render children()}
	</div>
	{#if advanced}
		<Collapse bind:open label="Advanced">
			<div class="fields advanced">
				{@render advanced()}
			</div>
		</Collapse>
	{/if}
</section>

<style>
	.form-section {
		display: flex;
		flex-direction: column;
		gap: 12px;
		min-width: 0;
	}
	.head {
		display: flex;
		gap: 8px;
		align-items: center;
	}
	.head h2 {
		font-size: var(--nebu-text-body);
	}
	.fields {
		display: flex;
		flex-direction: column;
		gap: 12px;
		min-width: 0;
	}
	.advanced {
		padding-top: 8px;
	}
</style>
