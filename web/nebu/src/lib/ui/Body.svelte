<script lang="ts">
	import JsonTree from '#lib/ui/JsonTree.svelte';
	import Lines from '#lib/ui/Lines.svelte';
	import Screen from '#lib/ui/Screen.svelte';

	// A body on a screen, a JSON object or list as a tree and anything else as its text
	let { text, maxHeight = '60vh' }: { text: string; maxHeight?: string } = $props();

	const parsed = $derived.by((): unknown => {
		try {
			const v: unknown = JSON.parse(text);
			return v !== null && typeof v === 'object' ? v : undefined;
		} catch {
			return undefined;
		}
	});
</script>

{#if parsed !== undefined}
	<Screen padding={8} {maxHeight}><JsonTree value={parsed} /></Screen>
{:else}
	<Lines {text} {maxHeight} />
{/if}
