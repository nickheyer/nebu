<script lang="ts">
	import type { Snippet } from 'svelte';

	// Two columns of cards in a fixed ratio once the page is wide enough, one column below that
	let { ratio = '3/2', left, right }: { ratio?: '1/1' | '3/2' | '2/1' | '2/3'; left: Snippet; right: Snippet } = $props();

	const tracks = $derived({ '1/1': 'minmax(0, 1fr) minmax(0, 1fr)', '3/2': 'minmax(0, 3fr) minmax(0, 2fr)', '2/1': 'minmax(0, 2fr) minmax(0, 1fr)', '2/3': 'minmax(0, 2fr) minmax(0, 3fr)' }[ratio]);
</script>

<div class="columnswrap">
	<div class="columns" style:--tracks={tracks}>
		<div class="col">{@render left()}</div>
		<div class="col">{@render right()}</div>
	</div>
</div>

<style>
	.columnswrap {
		min-width: 0;
		container-type: inline-size;
	}
	.columns {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: var(--nebu-space-5);
		align-items: start;
	}
	@container (min-width: 1000px) {
		.columns {
			grid-template-columns: var(--tracks);
		}
	}
	.col {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-5);
		min-width: 0;
	}
</style>
