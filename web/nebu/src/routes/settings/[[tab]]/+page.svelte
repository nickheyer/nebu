<script lang="ts">
	import { page } from '$app/state';
	import Settings from '#lib/pages/settings/Settings.svelte';
</script>

<svelte:head><title>Settings</title></svelte:head>
<Settings tab={page.params.tab ?? 'general'} />
