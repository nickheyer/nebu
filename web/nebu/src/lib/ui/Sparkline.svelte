<script lang="ts">
	let { points, width = 96, height = 20, phosphor = false }: { points: number[]; width?: number; height?: number; phosphor?: boolean } = $props();

	const path = $derived.by(() => {
		if (points.length < 2) return '';
		const max = Math.max(1, ...points);
		const step = width / (points.length - 1);
		return points.map((p, i) => `${i === 0 ? 'M' : 'L'}${(i * step).toFixed(1)},${(height - 1 - (p / max) * (height - 2)).toFixed(1)}`).join(' ');
	});
</script>

<svg class="spark" class:phosphor {width} {height} viewBox="0 0 {width} {height}" aria-hidden="true">
	<path d={path} fill="none" stroke-width="1.5" stroke-linejoin="round" stroke-linecap="round" />
</svg>

<style>
	.spark {
		display: block;
		flex: 0 0 auto;
	}
	.spark path {
		stroke: var(--nebu-steel);
	}
	.spark.phosphor path {
		stroke: var(--nebu-screen-text);
	}
</style>
