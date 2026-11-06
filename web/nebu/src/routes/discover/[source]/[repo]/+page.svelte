<script lang="ts">
	import { page } from '$app/state';
	import Repository from '#lib/pages/discover/Repository.svelte';
</script>

<svelte:head><title>{page.params.repo}</title></svelte:head>
{#key `${page.params.source}/${page.params.repo}`}
	<Repository sourceId={page.params.source ?? ''} repo={page.params.repo ?? ''} />
{/key}
