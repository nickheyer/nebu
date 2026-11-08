<script lang="ts">
	import type { DotShape, Lamp } from '#lib/ui/status.js';

	// A status lamp, filled for live states, half for starting ones, hollow for idle ones
	let { lamp = 'muted', shape = 'filled', size = 8, label }: { lamp?: Lamp; shape?: DotShape; size?: 6 | 8 | 10; label?: string } = $props();
</script>

<span class="dot {lamp} {shape}" style:width="{size}px" style:height="{size}px" role={label ? 'img' : undefined} aria-label={label}></span>

<style>
	.dot {
		display: inline-block;
		flex: 0 0 auto;
		box-sizing: border-box;
		border: 2px solid var(--dot);
		border-radius: 50%;
		background: var(--dot);
		transition:
			background var(--nebu-normal) var(--nebu-ease),
			border-color var(--nebu-normal) var(--nebu-ease);
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
		animation: nebu-breathe 1.8s ease-in-out infinite;
	}
</style>
