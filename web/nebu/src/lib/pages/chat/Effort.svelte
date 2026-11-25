<script lang="ts">
	import type { ModelEntry } from '#lib/api/gateway.js';
	import { chooseEffort, effortDefault, effortLevels, effortOf, effortOptions } from '#lib/pages/chat/effort.svelte.js';
	import Select from '#lib/ui/fields/Select.svelte';

	// How hard the chosen model reasons, beside the model it belongs to. Nothing for a model that takes no level
	let { model }: { model: ModelEntry | undefined } = $props();
</script>

{#if model && effortLevels(model).length > 0}
	<Select bind:value={() => effortOf(model) || undefined, (v) => chooseEffort(model, v)} options={effortOptions(model)} label="Reasoning" placeholder="Not set" clearable={effortDefault(model) === ''} compact fit />
{/if}
