<script lang="ts">
	import { page } from '$app/state';
	import Traffic from '#lib/pages/traffic/Traffic.svelte';
</script>

<svelte:head><title>Traffic</title></svelte:head>
<Traffic traceId={page.params.trace ?? ''} />
