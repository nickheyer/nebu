<script lang="ts">
	import Slider from '#lib/ui/fields/Slider.svelte';

	let {
		value = $bindable(0),
		max = 100,
		label,
		info,
		disabled = false,
		message,
		step = 1,
		onchange
	}: { value?: number; max?: number; label: string; info?: string; disabled?: boolean; message?: string; step?: number; onchange?: (n: number) => void } = $props();
</script>

<Slider bind:value={() => value, (n) => (value = n ?? 0)} min={0} {max} {step} unit="%" {label} {info} {disabled} {message} onchange={(n) => onchange?.(n ?? 0)} />
