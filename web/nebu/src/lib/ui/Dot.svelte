<script lang="ts">
	import type { DotShape, Lamp } from '#lib/ui/status.js';

	let { lamp = 'muted', shape = 'filled', size = 8, label }: { lamp?: Lamp; shape?: DotShape; size?: 6 | 8 | 10; label?: string } = $props();
</script>

<span class="dot {lamp} {shape}" style:width="{size}px" style:height="{size}px" role={label ? 'img' : undefined} aria-label={label}></span>

<style>
	.dot {
		display: inline-block;
		flex: 0 0 auto;
		border-radius: 50%;
		background: var(--dot);
		border: 2px solid var(--dot);
		box-sizing: border-box;
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
		--dot: var(--nebu-text-muted);
	}
	.hollow {
		background: transparent;
	}
	.half {
		background: linear-gradient(90deg, var(--dot) 50%, transparent 50%);
	}
</style>
