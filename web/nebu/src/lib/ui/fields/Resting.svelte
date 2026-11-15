<script lang="ts">
	import { capitalize } from '#lib/format.js';

	export type Offer = { label: string; pick: () => void };

	// The line under an optional control, what it rests on while unset, the named values it takes and the way back to unset when given one
	let { word = 'Default', text, offers = [], set = false, about = '', onreset }: { word?: string; text?: string; offers?: Offer[]; set?: boolean; about?: string; onreset?: () => void } = $props();
</script>

<div class="rest">
	{#if text}<span class="on">{word}: {text}</span>{/if}
	<span class="acts">
		{#each offers as o (o.label)}
			<button type="button" class="act" aria-label="{about}, {o.label}" onclick={o.pick}>{capitalize(o.label)}</button>
		{/each}
		{#if set && onreset}<button type="button" class="act" aria-label="Reset {about}" onclick={onreset}>Reset</button>{/if}
	</span>
</div>

<style>
	.rest {
		display: flex;
		flex-wrap: wrap;
		gap: 0 var(--nebu-space-3);
		align-items: center;
		justify-content: space-between;
		min-width: 0;
		min-height: 24px;
		font-size: var(--nebu-text-caption);
		color: var(--nebu-text-muted);
	}
	.on {
		min-width: 0;
		overflow-wrap: anywhere;
	}
	.acts {
		display: inline-flex;
		flex: 0 0 auto;
		gap: var(--nebu-space-3);
		margin-left: auto;
	}
	.act {
		min-height: 24px;
		margin: 0;
		padding: 0 2px;
		font: inherit;
		font-weight: 500;
		color: var(--neo-color-primary);
		background: none;
		border: 0;
		border-radius: 4px;
		cursor: pointer;
		appearance: none;
	}
	.act:hover {
		text-decoration: underline;
		text-underline-offset: 3px;
	}
	.act:focus-visible {
		outline: 2px solid var(--neo-color-primary);
		outline-offset: 1px;
	}
</style>
