<script lang="ts">
	import { page } from '$app/state';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoDialog } from '#lib/neo/floating/dialog/index.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { allowed, pageOf } from '#lib/shell/nav.js';
	import DotsThreeIcon from 'phosphor-svelte/lib/DotsThreeIcon';

	const items = $derived(allowed(session.role));
	const bar = $derived(items.filter((p) => p.bottom));
	const rest = $derived(items.filter((p) => !p.bottom));
	const current = $derived(pageOf(page.url.pathname)?.id);
	let more = $state(false);
	const moreActive = $derived(rest.some((p) => p.id === current));
</script>

<nav class="bottom" aria-label="Pages">
	{#each bar as item (item.id)}
		{@const active = current === item.id}
		<NeoButton text href={item.path} aria-current={active ? 'page' : undefined} color={active ? 'primary' : undefined} class="tab">
			<span class="tabbody"><item.icon size={20} /><span class="tablabel">{item.label}</span></span>
		</NeoButton>
	{/each}
	<NeoButton text onclick={() => (more = true)} color={moreActive ? 'primary' : undefined} class="tab">
		<span class="tabbody"><DotsThreeIcon size={20} /><span class="tablabel">More</span></span>
	</NeoButton>
</nav>

<NeoDialog bind:open={more} modal filled placement="bottom" rounded="md" elevation={2} padding="8px" full>
	<div class="sheet">
		{#each rest as item (item.id)}
			{@const active = current === item.id}
			<NeoButton text href={item.path} color={active ? 'primary' : undefined} onclick={() => (more = false)} class="row">
				{#snippet icon()}<item.icon size={20} />{/snippet}
				{item.label}
			</NeoButton>
		{/each}
	</div>
</NeoDialog>

<style>
	.bottom {
		display: flex;
		align-items: stretch;
		height: 56px;
		background: var(--neo-background-color);
		border-top: 1px solid var(--nebu-border);
	}
	.bottom :global(.tab) {
		flex: 1 1 0;
		min-width: 0;
	}
	.tabbody {
		display: flex;
		flex-direction: column;
		gap: 2px;
		align-items: center;
	}
	.tablabel {
		font-size: 12px;
	}
	.sheet {
		display: flex;
		flex-direction: column;
		gap: 2px;
		padding: 8px 0;
	}
	.sheet :global(.row) {
		justify-content: flex-start;
		min-height: 44px;
	}
</style>
