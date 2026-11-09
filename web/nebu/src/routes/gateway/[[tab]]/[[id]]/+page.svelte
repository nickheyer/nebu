<script lang="ts">
	import { page } from '$app/state';
	import Gateway from '#lib/pages/gateway/Gateway.svelte';
</script>

<svelte:head><title>Gateway</title></svelte:head>
<Gateway tab={page.params.tab ?? 'routes'} id={page.params.id ?? ''} />
