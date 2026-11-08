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
	import Str from '#lib/ui/Str.svelte';
	import SidebarSimpleIcon from 'phosphor-svelte/lib/SidebarSimpleIcon';

	const items = $derived(allowed(session.role));
	const current = $derived(pageOf(page.url.pathname)?.id);
	const collapsed = $derived(prefs.railCollapsed);

	// The label and hostname answered directly, standing in until the stream's snapshot carries them
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
	const meshName = $derived(live.mesh?.mesh?.name ?? '');
</script>

<nav class="rail" class:collapsed aria-label="Pages">
	<div class="brand">
		<Brand {collapsed} />
		{#if !collapsed}
			<div class="host">
				{#if !hostLabel && !asked}
					<Bone width="8ch" height="12px" />
				{:else}
					<Str value={hostLabel} kind="name" color="muted" width="100%" />
					{#if meshName}<span class="mesh"><Str value={meshName} kind="name" color="muted" width="100%" /></span>{/if}
				{/if}
			</div>
		{/if}
	</div>
	<div class="items">
		{#each items as item (item.id)}
			{@const active = current === item.id}
			{#if collapsed}
				<NeoTooltip tooltip={item.label} placement="right" elevation={2} filled triggerProps={{ tag: 'span', class: 'railtip' }}>
					<NeoButton text toggle checked={active} href={item.path} aria-label={item.label} aria-current={active ? 'page' : undefined} color={active ? 'primary' : undefined} class="railitem">
						{#snippet icon()}<item.icon size={20} />{/snippet}
					</NeoButton>
				</NeoTooltip>
			{:else}
				<NeoButton text toggle checked={active} href={item.path} aria-current={active ? 'page' : undefined} color={active ? 'primary' : undefined} justify="flex-start" class="railitem wide">
					{#snippet icon()}<item.icon size={20} />{/snippet}
					{item.label}
				</NeoButton>
			{/if}
		{/each}
	</div>
	<div class="foot">
		<NeoButton text aria-label={collapsed ? 'Show labels' : 'Hide labels'} onclick={() => (prefs.railCollapsed = !collapsed)}>
			{#snippet icon()}<SidebarSimpleIcon size={20} />{/snippet}
		</NeoButton>
	</div>
</nav>

<style>
	/* Holds the viewport height whatever the page scrolls, so the label toggle stays in reach */
	.rail {
		position: sticky;
		top: 0;
		display: flex;
		flex: 0 0 216px;
		flex-direction: column;
		align-self: flex-start;
		width: 216px;
		height: 100dvh;
		padding: 0 10px 12px;
		overflow-y: auto;
		background: var(--nebu-rail);
		border-right: 1px solid var(--nebu-border);
		transition:
			width 150ms ease-out,
			flex-basis 150ms ease-out;
	}
	.rail.collapsed {
		flex-basis: 60px;
		width: 60px;
	}
	/* Sits level with the top bar's title row */
	.brand {
		display: flex;
		flex-direction: column;
		gap: 2px;
		justify-content: center;
		height: 61px;
		margin: 0 -10px 12px;
		padding: 0 18px;
	}
	.collapsed .brand {
		align-items: center;
		padding: 0;
	}
	.host {
		display: flex;
		gap: 6px;
		align-items: baseline;
		min-width: 0;
		padding-left: 32px;
		font-size: var(--nebu-text-caption);
	}
	.mesh::before {
		content: '·';
		margin-right: 6px;
		color: var(--nebu-text-faint);
	}
	.mesh {
		display: inline-flex;
		min-width: 0;
	}
	.items {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		gap: 2px;
	}
	.items :global(.railtip) {
		display: flex;
	}
	.items :global(.neo-button.railitem) {
		justify-content: center;
		width: 100%;
		min-height: 38px;
	}
	.items :global(.neo-button.railitem.wide) {
		font-weight: 500;
	}
	.items :global(.neo-button.railitem.wide .neo-content) {
		justify-content: flex-start;
	}
	.foot {
		display: flex;
		justify-content: center;
		padding-top: 8px;
	}
	@media (prefers-reduced-motion: reduce) {
		.rail {
			transition: none;
		}
	}
</style>
