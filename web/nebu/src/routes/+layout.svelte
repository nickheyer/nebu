<script lang="ts">
	import '../theme.css';
	import '../app.css';
	import { onMount } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoNotificationProvider } from '#lib/neo/floating/notification/index.js';
	import { NeoPortalContainer } from '#lib/neo/floating/portal/index.js';
	import { NeoThemeProvider } from '#lib/neo/providers/index.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { live } from '#lib/live/events.svelte.js';
	import Login from '#lib/shell/Login.svelte';
	import NotifyBridge from '#lib/shell/NotifyBridge.svelte';
	import Shell from '#lib/shell/Shell.svelte';

	let { children: page } = $props();

	onMount(() => void session.load());

	$effect(() => {
		if (session.signedIn) live.start();
		else live.stop();
	});
</script>

<NeoThemeProvider>
	<NeoPortalContainer>
		<NeoNotificationProvider>
			{#snippet children(services)}
			<NotifyBridge {services} />
			{#if session.signedIn}
				<Shell>{@render page()}</Shell>
			{:else if session.ready}
				<Login />
			{:else}
				<div class="boot">
					{#if session.error}
						<p>Connection error: {session.error}</p>
						<NeoButton rounded onclick={() => session.load()}>Retry</NeoButton>
					{:else}
						<p>Connecting</p>
					{/if}
				</div>
			{/if}
			{/snippet}
		</NeoNotificationProvider>
	</NeoPortalContainer>
</NeoThemeProvider>

<style>
	.boot {
		display: grid;
		place-items: center;
		gap: var(--neo-gap);
		min-height: 100dvh;
	}
</style>
