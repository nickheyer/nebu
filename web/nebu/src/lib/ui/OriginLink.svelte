<script lang="ts">
	import type { Snippet } from 'svelte';

	// A name with where it comes from in small text on its baseline, the pair one link
	let { href, from, external = false, children }: { href: string; from: string; external?: boolean; children: Snippet } = $props();
</script>

<a class="origin" {href} target={external ? '_blank' : undefined} rel={external ? 'noreferrer' : undefined}>
	<span class="name">{@render children()}</span>
	{#if from}<span class="from">{from}</span>{/if}
</a>

<style>
	.origin {
		display: inline-flex;
		flex: 0 1 auto;
		gap: 6px;
		align-items: baseline;
		min-width: 0;
		color: inherit;
		text-decoration: none;
		transition: color var(--nebu-fast) var(--nebu-ease);
	}
	.name {
		display: inline-flex;
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.from {
		flex: 0 0 auto;
		font-size: var(--nebu-text-caption);
		font-weight: 400;
		color: var(--nebu-text-muted);
		white-space: nowrap;
		transition: color var(--nebu-fast) var(--nebu-ease);
	}
	.origin:hover,
	.origin:hover .from,
	.origin:focus-visible,
	.origin:focus-visible .from {
		color: var(--neo-color-primary);
	}
	.origin:hover .name,
	.origin:hover .from {
		text-decoration: underline;
		text-decoration-thickness: 1px;
		text-underline-offset: 3px;
	}
</style>
