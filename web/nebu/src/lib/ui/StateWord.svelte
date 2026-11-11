<script lang="ts">
	import type { Lamp } from '#lib/ui/status.js';

	// A state or a kind as one small word in the colour of its lamp
	let { label, lamp = 'muted' }: { label: string; lamp?: Lamp } = $props();
</script>

<span class="word {lamp}">{label}</span>

<style>
	.word {
		flex: 0 0 auto;
		font-size: var(--nebu-text-caption);
		white-space: nowrap;
	}
	.muted {
		color: var(--nebu-text-muted);
	}
	.success {
		color: var(--neo-color-success);
	}
	.warning {
		color: var(--neo-color-warning);
	}
	.error {
		color: var(--neo-color-error);
	}
	.primary {
		color: var(--neo-color-primary);
	}
</style>
