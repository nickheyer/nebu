<script lang="ts">
	import type { Segment } from '#lib/ui/Bar.svelte';

	// The fill a bar draws one kind of segment in, as a legend chip
	let { kind }: { kind: Segment['kind'] } = $props();
</script>

<span class="swatch {kind}" aria-hidden="true"></span>

<style>
	.swatch {
		display: inline-block;
		flex: 0 0 auto;
		width: 14px;
		height: 8px;
		border-radius: 2px;
	}
	.instance,
	.used {
		background: var(--neo-color-primary);
	}
	.reservation {
		background: repeating-linear-gradient(135deg, var(--neo-color-primary) 0 3px, transparent 3px 6px);
	}
	.planned {
		background: repeating-linear-gradient(135deg, var(--nebu-steel) 0 3px, transparent 3px 6px);
	}
	.other {
		background: var(--nebu-text-muted);
	}
	.warning {
		background: var(--neo-color-warning);
	}
	.free {
		background: var(--neo-background-color-secondary);
		box-shadow: var(--neo-box-shadow-inset-1);
	}
</style>
