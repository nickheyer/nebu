<script lang="ts">
	import { NeoInput } from '#lib/neo/inputs/index.js';
	import MagnifyingGlassIcon from 'phosphor-svelte/lib/MagnifyingGlassIcon';

	let { value = $bindable(''), placeholder = 'Search', delay = 250, width = '20rem' }: { value?: string; placeholder?: string; delay?: number; width?: string } = $props();

	let typed = $state(value);
	let timer: ReturnType<typeof setTimeout> | undefined;

	$effect(() => {
		typed = value;
	});

	function oninput() {
		clearTimeout(timer);
		timer = setTimeout(() => (value = typed), delay);
	}
</script>

<div class="search" style:width>
	<NeoInput bind:value={typed} {placeholder} elevation={-1} floating={false} clearable aria-label={placeholder} {oninput} onclear={() => (value = '')}>
		{#snippet before()}<MagnifyingGlassIcon size={16} />{/snippet}
	</NeoInput>
</div>

<style>
	.search {
		display: flex;
		max-width: 100%;
	}
	.search :global(.neo-input-group) {
		flex: 1 1 auto;
	}
</style>
