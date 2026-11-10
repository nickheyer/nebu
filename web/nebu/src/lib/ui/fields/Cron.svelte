<script lang="ts">
	import Select from '#lib/ui/fields/Select.svelte';
	import Text from '#lib/ui/fields/Text.svelte';

	const presets = [
		{ value: '* * * * *', label: 'Every minute' },
		{ value: '*/5 * * * *', label: 'Every 5 minutes' },
		{ value: '*/15 * * * *', label: 'Every 15 minutes' },
		{ value: '0 * * * *', label: 'Every hour' },
		{ value: '0 */6 * * *', label: 'Every 6 hours' },
		{ value: '0 9 * * *', label: 'Daily at 09:00' },
		{ value: '0 9 * * 1-5', label: 'Weekdays at 09:00' },
		{ value: '0 9 * * 1', label: 'Mondays at 09:00' },
		{ value: '0 0 1 * *', label: 'Monthly on the 1st' },
		{ value: 'custom', label: 'Custom' }
	];

	let { value = $bindable(''), label = 'Schedule', info, disabled = false }: { value?: string; label?: string; info?: string; disabled?: boolean } = $props();

	let preset = $state(presets.some((p) => p.value === value) ? value : value ? 'custom' : '');
	const custom = $derived(preset === 'custom');
	const fields = $derived(value.trim().split(/\s+/).filter(Boolean).length);
	const error = $derived(custom && value && fields !== 5 ? 'Five fields' : undefined);

	function picked(p: string | undefined) {
		if (!p) {
			value = '';
			return;
		}
		if (p !== 'custom') value = p;
	}
</script>

<div class="cron">
	<Select bind:value={preset} options={presets} {label} {info} {disabled} onchange={picked} />
	{#if custom}
		<Text bind:value label="Cron expression" info="Minute, hour, day of month, month, day of week" mono {disabled} {error} max={64} />
	{/if}
</div>

<style>
	.cron {
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
	}
</style>
