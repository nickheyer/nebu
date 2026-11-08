<script lang="ts">
	// One skeleton bar in the surface tone, round for a dot, grown to fill its line for a track
	let { width = '8ch', height = '12px', round = false, grow = false }: { width?: string; height?: string; round?: boolean; grow?: boolean } = $props();
</script>

<span class="bone" class:round class:grow style:width style:height aria-hidden="true"></span>

<style>
	.bone {
		display: inline-block;
		flex: 0 0 auto;
		max-width: 100%;
		background: var(--neo-skeleton-color);
		border-radius: 999px;
		animation: pulse 2s ease-in-out infinite;
	}
	.round {
		border-radius: 50%;
	}
	.grow {
		flex: 1 1 auto;
		min-width: 0;
	}
	@keyframes pulse {
		50% {
			opacity: 0.5;
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.bone {
			animation: none;
		}
	}
</style>
