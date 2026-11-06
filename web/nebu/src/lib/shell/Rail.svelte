<script lang="ts">
	import { page } from '$app/state';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoTooltip } from '#lib/neo/floating/tooltips/index.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { prefs } from '#lib/state/prefs.svelte.js';
	import { allowed, pageOf } from '#lib/shell/nav.js';
	import Brand from '#lib/shell/Brand.svelte';
	import SidebarSimpleIcon from 'phosphor-svelte/lib/SidebarSimpleIcon';

	const items = $derived(allowed(session.role));
	const current = $derived(pageOf(page.url.pathname)?.id);
	const collapsed = $derived(prefs.railCollapsed);
</script>

<nav class="rail" class:collapsed aria-label="Pages">
	<div class="brand"><Brand {collapsed} /></div>
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
	.rail {
		display: flex;
		flex: 0 0 200px;
		flex-direction: column;
		width: 200px;
		padding: 12px 8px;
		border-right: 1px solid var(--nebu-border);
		transition:
			width 150ms ease-out,
			flex-basis 150ms ease-out;
	}
	.rail.collapsed {
		flex-basis: 56px;
		width: 56px;
	}
	.brand {
		padding: 4px 8px 16px;
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
		min-height: 36px;
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
