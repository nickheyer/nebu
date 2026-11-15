<script lang="ts">
	import { NeoTooltip } from '#lib/neo/floating/tooltips/index.js';
	import InfoIcon from 'phosphor-svelte/lib/InfoIcon';

	// A button after a title or label that shows its help on hover, on a press, a tap or the keyboard, and holds it open once pressed
	let { text, about }: { text: string; about?: string } = $props();

	let open = $state(false);
	let pinned = $state(false);

	// A closed note is no longer pinned, however it closed
	$effect(() => {
		if (!open) pinned = false;
	});

	function press(e: MouseEvent) {
		e.preventDefault();
		e.stopPropagation();
		pinned = !pinned;
		open = pinned;
	}
</script>

<NeoTooltip bind:open placement="top" elevation={2} filled portal flipOptions={{ fallbackAxisSideDirection: 'none' }} openDelay={100} hoverDelay={200} openOnHover openOnFocus={false} openOnClick={false} keepOpenOnHover={pinned} padding="8px 12px" triggerProps={{ tag: 'span', class: 'infotip' }}>
	{#snippet tooltip()}
		<span class="note" role="note">{text}</span>
	{/snippet}
	<button type="button" class="info" class:pinned aria-label={about ? `About ${about}` : 'About this'} aria-expanded={open} onclick={press}><InfoIcon size={15} /></button>
</NeoTooltip>

<style>
	:global(.infotip) {
		display: inline-flex;
		flex: 0 0 auto;
	}
	/* The mark is small, the button around it large enough for a finger */
	.info {
		display: inline-grid;
		place-items: center;
		width: 24px;
		height: 24px;
		margin: 0 -4px;
		padding: 0;
		color: var(--nebu-text-muted);
		background: none;
		border: 0;
		border-radius: 50%;
		cursor: pointer;
		appearance: none;
		transition: color var(--nebu-fast) var(--nebu-ease);
	}
	.info:hover,
	.info.pinned {
		color: var(--neo-color-primary);
	}
	.info:focus-visible {
		outline: 2px solid var(--neo-color-primary);
		outline-offset: -2px;
	}
	.note {
		display: block;
		max-width: 40ch;
		font-size: 13px;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
</style>
