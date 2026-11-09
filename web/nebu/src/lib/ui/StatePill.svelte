<script lang="ts">
	import { NeoPill } from '#lib/neo/pill/index.js';
	import type { Lamp } from '#lib/ui/status.js';

	let { label, lamp = 'muted' }: { label: string; lamp?: Lamp } = $props();
	const color = $derived(lamp === 'muted' ? undefined : lamp);
</script>

<span class="pill" class:muted={lamp === 'muted'}>
	<NeoPill size="small" elevation={0} borderless={false} tinted {color} {label} />
</span>

<style>
	.pill {
		display: inline-flex;
		font-size: 12px;
		white-space: nowrap;
	}
	.pill.muted {
		color: var(--nebu-text-muted);
	}
</style>
