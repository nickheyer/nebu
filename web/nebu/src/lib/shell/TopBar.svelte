<script lang="ts">
	import { NeoBadge } from '#lib/neo/badge/index.js';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { live } from '#lib/live/events.svelte.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import AccountMenu from '#lib/shell/AccountMenu.svelte';
	import Brand from '#lib/shell/Brand.svelte';
	import Dot from '#lib/ui/Dot.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import ListChecksIcon from 'phosphor-svelte/lib/ListChecksIcon';

	const hostLabel = $derived(live.settings?.hostLabel || live.host?.hostname || '');
	const meshName = $derived(live.mesh?.mesh?.name ?? '');
	const active = $derived(live.activeTasks.length);
	const liveState = $derived(!session.reads ? 'off' : live.connected ? 'live' : live.error ? 'reconnecting' : 'connecting');
	const liveTip = $derived(liveState === 'live' ? 'Live' : liveState === 'reconnecting' ? `Reconnecting, ${live.error}` : 'Connecting');
	const liveLamp = $derived(liveState === 'live' ? 'success' : liveState === 'reconnecting' ? 'error' : liveState === 'connecting' ? 'warning' : 'muted');
</script>

{#snippet tasks()}
	<NeoButton href="/tasks" text aria-label="Tasks">
		{#snippet icon()}<ListChecksIcon size={20} />{/snippet}
	</NeoButton>
{/snippet}

<header class="bar">
	{#if viewport.mobile}
		<Brand />
	{/if}
	<Str value={hostLabel} kind="name" weight={500} />
	{#if meshName}
		<span class="secondary">·</span>
		<Str value={meshName} kind="name" color="muted" />
	{/if}
	<span class="fill"></span>
	{#if session.reads}
		{#if active > 0}
			<NeoBadge value={String(active)} placement="top-right" size="small" color="primary">
				{@render tasks()}
			</NeoBadge>
		{:else}
			{@render tasks()}
		{/if}
		<Tip text={liveTip} tag="span">
			<span class="live"><Dot lamp={liveLamp} label={liveState} /></span>
		</Tip>
	{/if}
	<AccountMenu />
</header>

<style>
	.bar {
		display: flex;
		gap: 12px;
		align-items: center;
		height: 48px;
		padding: 0 16px;
		border-bottom: 1px solid var(--nebu-border);
	}
	.live {
		display: inline-flex;
		padding: 0 4px;
	}
</style>
