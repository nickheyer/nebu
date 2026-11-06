<script lang="ts">
	import { page } from '$app/state';
	import Mesh from '#lib/pages/mesh/Mesh.svelte';
</script>

<svelte:head><title>Mesh</title></svelte:head>
<Mesh tab={page.params.tab ?? 'nodes'} id={page.params.id ?? ''} />
