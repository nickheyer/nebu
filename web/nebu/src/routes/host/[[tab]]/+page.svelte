<script lang="ts">
	import { page } from '$app/state';
	import Host from '#lib/pages/host/Host.svelte';
</script>

<svelte:head><title>Host</title></svelte:head>
<Host tab={page.params.tab === 'logs' ? 'logs' : 'overview'} />
