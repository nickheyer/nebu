<script lang="ts">
	import { useNeoThemeContext } from '#lib/neo/providers/neo-theme-provider-context.svelte.js';
	import { NeoTheme } from '#lib/neo/providers/neo-theme-provider.model.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import Columns from '#lib/ui/layout/Columns.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import Tokens from '#lib/pages/profile/Tokens.svelte';

	const theme = useNeoThemeContext();
	const themeChoice = $derived<'light' | 'dark' | 'system'>(theme.remember ? theme.theme : 'system');
	// Only an account signed in with a local password can change it here
	const ownPassword = $derived(session.status?.enabled === true && session.status.local !== null && session.user?.provider === 'local');

	let current = $state('');
	let fresh = $state('');
	let saving = $state(false);
	let error = $state<string | undefined>(undefined);

	function setTheme(v: 'light' | 'dark' | 'system') {
		if (v === 'system') {
			theme.update({ remember: false, theme: window.matchMedia('(prefers-color-scheme: dark)').matches ? NeoTheme.Dark : NeoTheme.Light });
			return;
		}
		theme.update({ remember: true, theme: v });
	}

	// Changes the password, which ends the session
	async function changePassword() {
		saving = true;
		error = undefined;
		try {
			await rpc.auth.setPassword({ username: session.user?.name ?? '', currentPassword: current, password: fresh });
			toast.success('Password changed', 'Sign in again');
			await session.load();
		} catch (err) {
			error = reason(err);
		} finally {
			saving = false;
		}
	}

	function discard() {
		current = '';
		fresh = '';
		error = undefined;
	}
</script>

<PageHeader title="Profile" />

<Columns ratio="2/3">
	{#snippet left()}
		{#if session.user}
			<Card title="Account">
				<KeyRows
					rows={[
						{ label: 'name', value: session.displayName },
						{ label: 'email', value: session.user.email },
						{ label: 'role', value: session.role },
						{ label: 'sign in', value: session.user.provider }
					]}
				/>
			</Card>
		{/if}
		<Card title="Appearance">
			<Segmented value={themeChoice} options={[{ value: 'light', label: 'Light' }, { value: 'dark', label: 'Dark' }, { value: 'system', label: 'System' }]} label="Theme" onchange={setTheme} />
		</Card>
		{#if ownPassword}
			<Card title="Password">
				<Secret bind:value={current} label="Current password" autocomplete="current-password" />
				<Secret bind:value={fresh} label="New password" autocomplete="new-password" />
				<SaveBar dirty={current !== '' || fresh !== ''} {saving} {error} label="Change" disabled={!current || !fresh} onsave={changePassword} ondiscard={discard} />
			</Card>
		{/if}
	{/snippet}
	{#snippet right()}
		<Tokens />
	{/snippet}
</Columns>
