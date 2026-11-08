<script lang="ts">
	import { page } from '$app/state';
	import Runtimes from '#lib/pages/runtimes/Runtimes.svelte';

	const tabs = ['installs', 'install', 'builds', 'defaults'];
</script>

<svelte:head><title>Runtimes</title></svelte:head>
<Runtimes id={page.params.id ?? ''} tab={tabs.includes(page.params.tab ?? '') ? (page.params.tab ?? 'installs') : 'installs'} />
