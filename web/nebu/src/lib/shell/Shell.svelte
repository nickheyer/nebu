<script lang="ts">
	import type { Snippet } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoTab, NeoTabs } from '#lib/neo/nav/index.js';
	import { NeoPill } from '#lib/neo/pill/index.js';
	import { NeoThemeSelector } from '#lib/neo/providers/index.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { live } from '#lib/live/events.svelte.js';

	let { children }: { children: Snippet } = $props();

	const sections = [
		{ id: '/', label: 'Overview' },
		{ id: '/models', label: 'Models' },
		{ id: '/instances', label: 'Instances' },
		{ id: '/sources', label: 'Sources' },
		{ id: '/runtimes', label: 'Runtimes' },
		{ id: '/tasks', label: 'Tasks' },
		{ id: '/settings', label: 'Settings' },
		{ id: '/access', label: 'Access' }
	];
	const active = $derived('/' + page.url.pathname.split('/')[1]);

	function navigate(id?: string) {
		if (id && id !== active) void goto(id);
	}
</script>

<div class="shell">
	<header class="bar">
		<a class="brand" href="/">nebu</a>
		<NeoTabs rounded pressed dim tag="nav" {active} onchange={navigate}>
			{#each sections as s (s.id)}
				<NeoTab tabId={s.id}>{s.label}</NeoTab>
			{/each}
		</NeoTabs>
		<div class="status">
			{#if !live.connected}
				<NeoPill color="warning" tinted label={live.error ? 'reconnecting' : 'connecting'} />
			{/if}
			{#if session.status?.enabled && session.user}
				<span class="who">{session.user.name || session.user.email || session.user.subject}</span>
				<NeoPill tinted label={session.user.role} />
				<NeoButton text loading={session.busy} onclick={() => session.logout()}>Sign out</NeoButton>
			{/if}
			<NeoThemeSelector />
		</div>
	</header>
	<main class="content">
		{@render children()}
	</main>
</div>

<style>
	.shell {
		display: flex;
		flex-direction: column;
		min-height: 100dvh;
	}
	.bar {
		display: flex;
		align-items: center;
		gap: var(--neo-gap-lg);
		padding: var(--neo-gap) var(--neo-gap-lg);
	}
	.brand {
		font-size: var(--neo-font-size-xl);
		font-weight: 700;
		color: inherit;
		text-decoration: none;
	}
	.status {
		display: flex;
		align-items: center;
		gap: var(--neo-gap-sm);
		margin-left: auto;
	}
	.who {
		color: var(--neo-text-color-secondary);
	}
	.content {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		gap: var(--neo-gap-lg);
		padding: 0 var(--neo-gap-lg) var(--neo-gap-xl);
	}
</style>
