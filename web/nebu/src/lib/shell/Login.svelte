<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoCard } from '#lib/neo/cards/index.js';
	import { NeoInput } from '#lib/neo/inputs/index.js';
	import { session } from '#lib/auth/session.svelte.js';

	let username = $state('');
	let password = $state('');
	const status = $derived(session.status);
	const setup = $derived(status?.local?.setup === true);
	const local = $derived(status?.local !== null && status?.local !== undefined);

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		if (setup) await session.setup({ username, password });
		else await session.login({ username, password });
	}
</script>

<main class="login">
	<NeoCard rounded elevation={3} width="min(24rem, 92vw)" spacing="2rem">
		<h1>nebu</h1>
		{#if local}
			<p class="hint">{setup ? 'No account exists yet. The one you create now is the admin.' : 'Sign in with your local account.'}</p>
			<form onsubmit={submit}>
				<NeoInput bind:value={username} label="Username" autocomplete="username" required />
				<NeoInput type="password" bind:value={password} label="Password" autocomplete={setup ? 'new-password' : 'current-password'} required />
				{#if session.error}<p class="error">{session.error}</p>{/if}
				<NeoButton type="submit" filled color="primary" loading={session.busy}>{setup ? 'Create account' : 'Sign in'}</NeoButton>
			</form>
		{/if}
		{#if status?.sso}
			<NeoButton href="/auth/oidc/login?next=/" rounded>Continue with {status.sso.name}</NeoButton>
		{/if}
		{#if !local && !status?.sso}
			<p class="error">Authentication is on but no sign-in method is configured.</p>
		{/if}
	</NeoCard>
</main>

<style>
	.login {
		display: grid;
		place-items: center;
		min-height: 100dvh;
	}
	h1 {
		margin: 0 0 var(--neo-gap);
		text-align: center;
	}
	.hint {
		margin: 0 0 var(--neo-gap);
		color: var(--neo-text-color-secondary);
	}
	form {
		display: flex;
		flex-direction: column;
		gap: var(--neo-gap);
	}
	.error {
		margin: 0;
		color: var(--neo-color-error);
	}
</style>
