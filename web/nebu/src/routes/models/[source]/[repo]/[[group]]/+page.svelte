<script lang="ts">
	import { page } from '$app/state';
	import Models from '#lib/pages/models/Models.svelte';
</script>

<svelte:head><title>Models</title></svelte:head>
<Models sourceId={page.params.source} repo={page.params.repo} group={page.params.group ?? ''} />
