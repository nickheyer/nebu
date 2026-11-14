<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoTooltip } from '#lib/neo/floating/tooltips/index.js';
	import { rpc } from '#lib/api/transport.js';
	import { live } from '#lib/live/events.svelte.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { prefs } from '#lib/state/prefs.svelte.js';
	import { allowed, pageOf } from '#lib/shell/nav.js';
	import Brand from '#lib/shell/Brand.svelte';
	import Bone from '#lib/ui/Bone.svelte';
	import SidebarSimpleIcon from 'phosphor-svelte/lib/SidebarSimpleIcon';

	const items = $derived(allowed(session.role));
	const current = $derived(pageOf(page.url.pathname)?.id);
	const collapsed = $derived(prefs.railCollapsed);

	// The label and hostname asked directly until the stream carries them
	let askedLabel = $state('');
	let askedHost = $state('');
	let asked = $state(false);

	onMount(async () => {
		if (!session.reads) {
			asked = true;
			return;
		}
		const [settings, profile] = await Promise.allSettled([rpc.settings.getSettings({}), rpc.host.getProfile({})]);
		if (settings.status === 'fulfilled') askedLabel = settings.value.settings?.hostLabel ?? '';
		if (profile.status === 'fulfilled') askedHost = profile.value.profile?.hostname ?? '';
		asked = true;
	});

	const hostLabel = $derived(live.settings?.hostLabel || live.host?.hostname || askedLabel || askedHost);
</script>

<nav class="rail" class:collapsed aria-label="Pages">
	<div class="brand">
		{#if collapsed}
			<Brand size={32} />
		{:else if !hostLabel && !asked}
			<Brand size={32} />
			<Bone width="9ch" height="14px" />
		{:else}
			<Brand name={hostLabel} size={32} />
		{/if}
	</div>
	<div class="items">
		{#each items as item, i (item.id)}
			{@const active = current === item.id}
			<span class="item" style:--i={i}>
				{#if collapsed}
					<NeoTooltip tooltip={item.label} placement="right" elevation={2} filled triggerProps={{ tag: 'span', class: 'railtip' }}>
						<NeoButton text toggle checked={active} href={item.path} aria-label={item.label} aria-current={active ? 'page' : undefined} color={active ? 'primary' : undefined} class="railitem">
							{#snippet icon()}<item.icon size={22} />{/snippet}
						</NeoButton>
					</NeoTooltip>
				{:else}
					<NeoButton text toggle checked={active} href={item.path} aria-current={active ? 'page' : undefined} color={active ? 'primary' : undefined} justify="flex-start" class="railitem wide">
						{#snippet icon()}<item.icon size={22} />{/snippet}
						{item.label}
					</NeoButton>
				{/if}
			</span>
		{/each}
	</div>
	<div class="foot">
		<NeoButton text aria-label={collapsed ? 'Show labels' : 'Hide labels'} onclick={() => (prefs.railCollapsed = !collapsed)}>
			{#snippet icon()}<SidebarSimpleIcon size={20} />{/snippet}
		</NeoButton>
	</div>
</nav>

<style>
	.rail {
		position: sticky;
		top: 0;
		display: flex;
		flex: 0 0 var(--nebu-rail-width);
		flex-direction: column;
		align-self: flex-start;
		width: var(--nebu-rail-width);
		height: 100dvh;
		padding: 0 12px 12px;
		overflow-y: auto;
		background: var(--nebu-rail);
		border-right: 1px solid var(--nebu-border);
		transition:
			width 180ms var(--nebu-settle),
			flex-basis 180ms var(--nebu-settle);
	}
	.brand {
		display: flex;
		gap: 10px;
		align-items: center;
		height: 61px;
		margin: 0 -12px 16px;
		padding: 0 18px;
	}
	.collapsed .brand {
		justify-content: center;
		padding: 0;
	}
	.items {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		gap: 4px;
	}
	.item {
		display: flex;
		animation: nebu-reveal 260ms var(--nebu-settle) backwards;
		animation-delay: calc(var(--i) * 24ms);
	}
	.items :global(.railtip) {
		display: flex;
		flex: 1 1 auto;
	}
	.items :global(.neo-button.railitem) {
		justify-content: center;
		width: 100%;
		min-height: 46px;
		font-size: 16px;
		font-weight: 400;
	}
	.items :global(.neo-button.railitem.wide) {
		--neo-btn-padding: 0.5rem 0.875rem;
	}
	.items :global(.neo-button.railitem.wide .neo-content) {
		gap: 0.75rem;
	}
	.items :global(.neo-button.railitem.wide .neo-content) {
		justify-content: flex-start;
	}
	.foot {
		display: flex;
		justify-content: center;
		padding-top: 8px;
	}
</style>
