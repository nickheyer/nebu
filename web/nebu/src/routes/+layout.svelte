<script lang="ts">
	import '../theme.css';
	import '../app.css';
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoLoadingMatrix } from '#lib/neo/loading/index.js';
	import { NeoNotificationProvider } from '#lib/neo/floating/notification/index.js';
	import { NeoPortalContainer } from '#lib/neo/floating/portal/index.js';
	import { NeoThemeProvider } from '#lib/neo/providers/index.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { live } from '#lib/live/events.svelte.js';
	import { home, pageOf } from '#lib/shell/nav.js';
	import NotifyBridge from '#lib/shell/NotifyBridge.svelte';
	import Shell from '#lib/shell/Shell.svelte';

	let { children: content } = $props();

	const atLogin = $derived(page.url.pathname === '/login');

	onMount(() => void session.load());

	$effect(() => {
		if (session.signedIn && session.reads) live.start();
		else live.stop();
	});

	// Sends a signed out browser to login and a signed in one away from it, keeping the page asked for
	$effect(() => {
		if (!session.ready) return;
		if (!session.signedIn && !atLogin) {
			const next = page.url.pathname + page.url.search;
			void goto(`/login${next !== '/' ? `?next=${encodeURIComponent(next)}` : ''}`, { replace: true });
			return;
		}
		if (session.signedIn && atLogin) {
			const next = page.url.searchParams.get('next');
			void goto(next && next.startsWith('/') ? next : home(session.role), { replace: true });
			return;
		}
		if (session.signedIn) {
			const target = pageOf(page.url.pathname);
			if (target && !target.roles.includes(session.role)) void goto(home(session.role), { replace: true });
		}
	});
</script>

<NeoThemeProvider>
	<NeoPortalContainer>
		<NeoNotificationProvider>
			{#snippet children(services)}
				<NotifyBridge {services} />
				{#if session.ready && (atLogin || session.signedIn)}
					{#if atLogin}
						{@render content()}
					{:else}
						<Shell>{@render content()}</Shell>
					{/if}
				{:else}
					<div class="boot">
						{#if session.error}
							<p class="error-text">{session.error}</p>
							<NeoButton elevation={2} onclick={() => session.load()}>Retry</NeoButton>
						{:else}
							<NeoLoadingMatrix aria-label="Connecting" width="48px" height="48px" />
						{/if}
					</div>
				{/if}
			{/snippet}
		</NeoNotificationProvider>
	</NeoPortalContainer>
</NeoThemeProvider>

<style>
	.boot {
		display: flex;
		flex-direction: column;
		gap: 16px;
		align-items: center;
		justify-content: center;
		min-height: 100dvh;
	}
</style>
