<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoCard } from '#lib/neo/cards/index.js';
	import { NeoDivider } from '#lib/neo/divider/index.js';
	import { NeoLoadingMatrix } from '#lib/neo/loading/index.js';
	import { reason } from '#lib/api/transport.js';
	import { session } from '#lib/auth/session.svelte.js';
	import Brand from '#lib/shell/Brand.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';

	let username = $state('');
	let password = $state('');
	let again = $state('');
	let mismatch = $state(false);
	let failure = $state('');

	const status = $derived(session.status);
	const setup = $derived(status?.local?.setup === true);
	const local = $derived(status?.local !== null && status?.local !== undefined);
	const sso = $derived(status?.sso ?? null);
	const next = $derived.by(() => {
		const n = new URLSearchParams(location.search).get('next');
		return n && n.startsWith('/') ? n : '/';
	});
	const refused = $derived(failure || session.error || '');

	// Signs in or creates the first account, clearing the password when the daemon refuses
	async function submit(event: SubmitEvent) {
		event.preventDefault();
		mismatch = false;
		failure = '';
		if (setup && password !== again) {
			mismatch = true;
			again = '';
			return;
		}
		try {
			const ok = setup ? await session.setup({ username, password }) : await session.login({ username, password });
			if (ok) return;
		} catch (err) {
			failure = reason(err);
		}
		password = '';
		again = '';
	}
</script>

<main class="login">
	<Brand />
	{#if !session.ready}
		{#if session.error}
			<Stack gap={12}>
				<p class="error-text">{session.error}</p>
				<NeoButton elevation={2} onclick={() => session.load()}>Retry</NeoButton>
			</Stack>
		{:else}
			<NeoLoadingMatrix aria-label="Connecting" width="48px" height="48px" />
		{/if}
	{:else if status?.enabled}
		<div class="card">
			<NeoCard rounded="md" elevation={2} spacing="24px" width="100%">
				<Stack gap={16}>
					{#if setup}<h2 class="title">Create the first account</h2>{/if}
					{#if local}
						<form onsubmit={submit}>
							<Stack gap={16}>
								<Text bind:value={username} label="Username" min={1} max={64} autocomplete="username" />
								<Secret bind:value={password} label="Password" autocomplete={setup ? 'new-password' : 'current-password'} />
								{#if setup}
									<Secret bind:value={again} label="Password again" autocomplete="new-password" error={mismatch ? 'Passwords differ' : undefined} />
								{/if}
								{#if refused}<p class="error-text secondary">{refused}</p>{/if}
								<div class="actions">
									<NeoButton type="submit" class="primary" elevation={2} loading={session.busy}>{setup ? 'Create account' : 'Sign in'}</NeoButton>
								</div>
							</Stack>
						</form>
					{/if}
					{#if local && sso}
						<div class="or">
							<NeoDivider elevation={0} flex="1 1 auto" />
							<span class="secondary">or</span>
							<NeoDivider elevation={0} flex="1 1 auto" />
						</div>
					{/if}
					{#if sso}
						<div class="actions">
							<NeoButton href="/auth/oidc/login?next={encodeURIComponent(next)}" elevation={2}>Continue with {sso.name}</NeoButton>
						</div>
					{/if}
				</Stack>
			</NeoCard>
		</div>
	{/if}
</main>

<style>
	.login {
		display: flex;
		flex-direction: column;
		gap: 24px;
		align-items: center;
		justify-content: center;
		min-height: 100dvh;
		padding: 16px;
	}
	.card {
		display: flex;
		width: 360px;
		max-width: 100%;
	}
	.title {
		text-align: center;
	}
	.actions {
		display: flex;
		justify-content: center;
	}
	.or {
		display: flex;
		gap: 12px;
		align-items: center;
	}
</style>
