<script lang="ts">
	import { page } from '$app/state';
	import Tasks from '#lib/pages/tasks/Tasks.svelte';
</script>

<svelte:head><title>Tasks</title></svelte:head>
<Tasks id={page.params.id ?? ''} />
