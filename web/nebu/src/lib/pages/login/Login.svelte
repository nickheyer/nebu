<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason } from '#lib/api/transport.js';
	import { session } from '#lib/auth/session.svelte.js';
	import Brand from '#lib/shell/Brand.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Card from '#lib/ui/layout/Card.svelte';

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

	// Signs in or creates the first account
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

{#snippet ssoButton(primary: boolean)}
	<NeoButton href="/auth/oidc/login?next={encodeURIComponent(next)}" elevation={primary ? 2 : 1} class={primary ? 'primary wide' : 'wide'}>Continue with {sso?.name}</NeoButton>
{/snippet}

{#snippet ssoFooter()}{@render ssoButton(false)}{/snippet}

<main class="login">
	{#if status?.enabled}
		<div class="column">
			<span class="brand"><Brand size={40} /></span>
			<Card title={setup ? 'Create the first account' : undefined} footer={local && sso ? ssoFooter : undefined}>
				{#if local}
					<form class="form" onsubmit={submit}>
						<Text bind:value={username} label="Username" info="Your account name" min={1} autocomplete="username" />
						<Secret bind:value={password} label="Password" info={setup ? 'Password for the first account' : 'Your password'} autocomplete={setup ? 'new-password' : 'current-password'} />
						{#if setup}
							<Secret bind:value={again} label="Password again" info="Typed again to catch typos" autocomplete="new-password" error={mismatch ? 'Passwords differ' : undefined} />
						{/if}
						<div class="commit">
							<p class="error-text secondary refused" aria-live="assertive">{refused}</p>
							<NeoButton type="submit" class="primary wide" elevation={2} loading={session.busy}>{setup ? 'Create account' : 'Sign in'}</NeoButton>
						</div>
					</form>
				{:else if sso}
					{@render ssoButton(true)}
				{/if}
			</Card>
		</div>
	{/if}
</main>

<style>
	.login {
		display: flex;
		align-items: center;
		justify-content: center;
		min-height: 100dvh;
		padding: var(--nebu-space-4);
	}
	.column {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-5);
		width: 100%;
		max-width: 360px;
	}
	.brand {
		display: flex;
		justify-content: center;
		animation: nebu-reveal 320ms var(--nebu-settle) backwards;
	}
	.column > :global(.card) {
		--nebu-settle-step: 120ms;
	}
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-4);
	}
	.commit {
		display: flex;
		flex-direction: column;
		padding-top: var(--nebu-space-2);
	}
	.refused:not(:empty) {
		margin-bottom: var(--nebu-space-3);
	}
	.login :global(.neo-button.wide) {
		flex: 1 1 auto;
		width: 100%;
	}
</style>
