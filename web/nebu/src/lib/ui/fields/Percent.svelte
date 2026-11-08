<script lang="ts">
	import Slider from '#lib/ui/fields/Slider.svelte';

	let {
		value = $bindable(0),
		max = 100,
		label,
		disabled = false,
		message,
		step = 1,
		onchange
	}: { value?: number; max?: number; label?: string; disabled?: boolean; message?: string; step?: number; onchange?: (n: number) => void } = $props();
</script>

<Slider bind:value min={0} {max} {step} unit="%" {label} {disabled} {message} {onchange} />
