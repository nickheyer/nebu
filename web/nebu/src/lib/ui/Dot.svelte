<script lang="ts">
	import type { DotShape, Lamp } from '#lib/ui/status.js';

	// A status lamp. Shape carries the state alongside colour: filled live, half starting, hollow idle.
	// Pass `label` so assistive tech reads the state, and `text` to print it beside the dot.
	let {
		lamp = 'muted',
		shape = 'filled',
		size = 8,
		label,
		text
	}: { lamp?: Lamp; shape?: DotShape; size?: 6 | 8 | 10; label?: string; text?: string } = $props();
</script>

{#if text}
	<span class="lamp">
		<span class="dot {lamp} {shape}" style:width="{size}px" style:height="{size}px" aria-hidden="true"></span>
		<span class="text">{text}</span>
	</span>
{:else}
	<span class="dot {lamp} {shape}" style:width="{size}px" style:height="{size}px" role={label ? 'img' : undefined} aria-label={label}></span>
{/if}

<style>
	.lamp {
		display: inline-flex;
		flex: 0 0 auto;
		gap: 6px;
		align-items: center;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
	.dot {
		display: inline-block;
		flex: 0 0 auto;
		box-sizing: border-box;
		border: 2px solid var(--dot);
		border-radius: 50%;
		background: var(--dot);
	}
	.success {
		--dot: var(--neo-color-success);
	}
	.warning {
		--dot: var(--neo-color-warning);
	}
	.error {
		--dot: var(--neo-color-error);
	}
	.primary {
		--dot: var(--neo-color-primary);
	}
	.muted {
		--dot: var(--nebu-text-faint);
	}
	.hollow {
		background: transparent;
	}
	.half {
		background: linear-gradient(90deg, var(--dot) 50%, transparent 50%);
	}
</style>
