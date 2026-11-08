<script lang="ts">
	// One skeleton bar, round for a dot
	let { width = '8ch', height = '12px', round = false, grow = false }: { width?: string; height?: string; round?: boolean; grow?: boolean } = $props();
</script>

<span class="bone" class:round class:grow style:width style:height aria-hidden="true"></span>

<style>
	.bone {
		display: inline-block;
		flex: 0 0 auto;
		max-width: 100%;
		background: linear-gradient(90deg, var(--neo-skeleton-color) 0%, var(--neo-skeleton-color) 40%, color-mix(in srgb, var(--neo-color-primary) 22%, var(--neo-skeleton-color)) 50%, var(--neo-skeleton-color) 60%, var(--neo-skeleton-color) 100%);
		background-size: 200% 100%;
		border-radius: 999px;
		animation: nebu-scan 1.6s linear infinite;
	}
	.round {
		border-radius: 50%;
	}
	.grow {
		flex: 1 1 auto;
		min-width: 0;
	}
	@media (prefers-reduced-motion: reduce) {
		.bone {
			animation: none;
		}
	}
</style>
