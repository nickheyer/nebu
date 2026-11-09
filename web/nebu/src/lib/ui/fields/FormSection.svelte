<script lang="ts">
	import type { Snippet } from 'svelte';
	import Collapse from '#lib/ui/Collapse.svelte';
	import Tip from '#lib/ui/Tip.svelte';

	let {
		title,
		info,
		children,
		advanced,
		// Folds the whole section under its title, for one holding only seldom touched fields
		collapsible = false
	}: { title?: string; info?: string; children: Snippet; advanced?: Snippet; collapsible?: boolean } = $props();
	let open = $state(false);
	// Each disclosure names its own section, so several in one form read apart
	const more = $derived(title ? `Advanced ${title.toLowerCase()}` : 'Advanced');
</script>

<section class="form-section">
	{#if collapsible && title}
		<Collapse bind:open label={title} heading indent={false}>
			<div class="fields">
				{@render children()}
			</div>
		</Collapse>
	{:else}
		{#if title}
			<div class="head" class:informed={info !== undefined}>
				<Tip text={info} tag="span"><h2>{title}</h2></Tip>
			</div>
		{/if}
		<div class="fields">
			{@render children()}
		</div>
		{#if advanced}
			<Collapse bind:open label={more} indent={false}>
				<div class="fields advanced">
					{@render advanced()}
				</div>
			</Collapse>
		{/if}
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
	.informed h2 {
		text-decoration: underline dotted var(--nebu-text-faint);
		text-decoration-thickness: 1px;
		text-underline-offset: 4px;
		cursor: help;
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
