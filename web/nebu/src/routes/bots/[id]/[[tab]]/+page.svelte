<script lang="ts">
	import { page } from '$app/state';
	import { live } from '#lib/live/events.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import BotPage from '#lib/pages/bots/BotPage.svelte';
	import RobotIcon from 'phosphor-svelte/lib/RobotIcon';

	const bot = $derived(live.bots.get(page.params.id ?? ''));
</script>

<svelte:head><title>{bot?.name ?? 'Bot'}</title></svelte:head>
{#if bot}
	{#key bot.id}
		<BotPage {bot} tab={page.params.tab ?? 'status'} />
	{/key}
{:else}
	<PageHeader title="Bot" back={{ label: 'Bots', href: '/bots' }} />
	<Card>
		{#if live.ready}
			<Empty icon={RobotIcon} text="No such bot" hint="It was deleted, or the link names a bot this daemon never had" action="Bots" href="/bots" />
		{:else}
			<Skeleton rows={3} kind="facts" />
		{/if}
	</Card>
{/if}
