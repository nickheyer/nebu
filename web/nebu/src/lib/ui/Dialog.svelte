<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoDialog } from '#lib/neo/floating/dialog/index.js';
	import { viewport } from '#lib/state/viewport.svelte.js';

	// A modal form with a title, fields that scroll when taller than the window, Cancel and one primary action
	let {
		open = $bindable(false),
		title,
		confirm,
		busy = false,
		disabled = false,
		danger = false,
		width = 'narrow',
		cancel = 'Cancel',
		// Stays open when the page behind it is pressed
		persistent = false,
		onconfirm,
		onclose,
		children,
		footer
	}: {
		open?: boolean;
		title: string;
		confirm?: string;
		busy?: boolean;
		disabled?: boolean;
		danger?: boolean;
		width?: 'narrow' | 'wide' | 'full' | 'broad';
		cancel?: string;
		persistent?: boolean;
		onconfirm?: () => unknown;
		onclose?: () => void;
		children: Snippet;
		footer?: Snippet;
	} = $props();

	const widths = { narrow: 'min(28rem, 92vw)', wide: 'min(36rem, 92vw)', full: 'min(48rem, 92vw)', broad: 'min(72rem, 94vw)' };
	let above = $state(false);
	let below = $state(false);

	// Follows which ends of the scrolling body hold more than it shows, by the marks at each end of the form
	function edges(node: HTMLElement) {
		const [top, bottom] = node.querySelectorAll(':scope > .content > .mark');
		const io = new IntersectionObserver(
			(entries) => {
				for (const e of entries) {
					if (e.target === top) above = !e.isIntersecting;
					else below = !e.isIntersecting;
				}
			},
			{ root: node }
		);
		io.observe(top!);
		io.observe(bottom!);
		return () => io.disconnect();
	}

	function close() {
		open = false;
		onclose?.();
	}
</script>

<NeoDialog bind:open modal rounded="md" filled elevation={2} width={widths[width]} style="--nebu-dialog-width: {widths[width]}" padding="0" slide={!viewport.reducedMotion} closeOnClickOutside={!persistent} onclose={() => onclose?.()}>
	<div class="dialog">
		<h2 class="title">{title}</h2>
		<div class="body" class:above class:below {@attach edges}>
			<div class="content">
				<span class="mark"></span>
				{@render children()}
				<span class="mark end"></span>
			</div>
		</div>
		<div class="footer">
			{#if footer}<div class="status">{@render footer()}</div>{/if}
			<span class="fill"></span>
			<NeoButton text onclick={close}>{confirm ? cancel : 'Close'}</NeoButton>
			{#if confirm}
				<NeoButton class={danger ? 'danger' : 'primary'} elevation={2} loading={busy} {disabled} onclick={() => onconfirm?.()}>{confirm}</NeoButton>
			{/if}
		</div>
	</div>
</NeoDialog>

<style>
	.dialog {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		gap: var(--nebu-space-4);
		min-width: 0;
		padding: var(--nebu-space-5);
	}
	.title {
		font-size: var(--nebu-text-heading);
	}
	/* A tall form scrolls under the title, its bar on the dialog's own edge */
	.body {
		min-width: 0;
		max-height: calc(100dvh - 18rem);
		margin-right: calc(-1 * var(--nebu-space-5));
		padding-right: var(--nebu-space-5);
		overflow-x: hidden;
		overflow-y: auto;
	}
	.content {
		position: relative;
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-3);
		min-width: 0;
	}
	.mark {
		position: absolute;
		top: 0;
		left: 0;
		width: 1px;
		height: 1px;
		pointer-events: none;
	}
	.mark.end {
		top: auto;
		bottom: 0;
	}
	/* An end with more form past it fades out, so a cut row reads as continuing */
	.body.above {
		mask-image: linear-gradient(to bottom, transparent, #000 var(--nebu-space-5));
	}
	.body.below {
		mask-image: linear-gradient(to top, transparent, #000 var(--nebu-space-5));
	}
	.body.above.below {
		mask-image: linear-gradient(to bottom, transparent, #000 var(--nebu-space-5), #000 calc(100% - var(--nebu-space-5)), transparent);
	}
	.footer {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		min-width: 0;
		padding-top: var(--nebu-space-2);
	}
	.status {
		display: flex;
		min-width: 0;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
	}
</style>
