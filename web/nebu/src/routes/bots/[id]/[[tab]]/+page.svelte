<script lang="ts">
	import { page } from '$app/state';
	import { live } from '#lib/live/events.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import BotPage from '#lib/pages/bots/BotPage.svelte';

	const bot = $derived(live.bots.get(page.params.id ?? ''));
</script>

<svelte:head><title>{bot?.name ?? 'Bot'}</title></svelte:head>
{#if bot}
	{#key bot.id}
		<BotPage {bot} tab={page.params.tab ?? 'status'} />
	{/key}
{:else if live.ready}
	<Empty text="No such bot" action="Bots" href="/bots" />
{:else}
	<Skeleton rows={3} />
{/if}
