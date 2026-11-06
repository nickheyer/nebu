<script lang="ts">
	import { page } from '$app/state';
	import Serve from '#lib/pages/serve/Serve.svelte';
</script>

<svelte:head><title>Serve</title></svelte:head>
<Serve instanceId={page.params.instance ?? ''} />
