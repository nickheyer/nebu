<script lang="ts">
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoMenu } from '#lib/neo/floating/menu/index.js';
	import type { NeoMenuItem } from '#lib/neo/floating/menu/index.js';
	import { useNeoThemeContext } from '#lib/neo/providers/neo-theme-provider-context.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import UserIcon from 'phosphor-svelte/lib/UserIcon';

	const theme = useNeoThemeContext();
	// Only an account signed in with a local password can change it here
	const ownPassword = $derived(session.status?.enabled === true && session.status.local !== null && session.user?.provider === 'local');
	const items = $derived.by((): NeoMenuItem[] => {
		const list: NeoMenuItem[] = [];
		if (session.status?.enabled && session.user) {
			list.push({ value: 'who', label: session.displayName, description: session.role, readonly: true });
		}
		list.push({ value: 'theme', label: theme.theme === 'dark' ? 'Light theme' : 'Dark theme', divider: list.length > 0 });
		list.push({ value: 'tokens', label: 'API tokens' });
		if (ownPassword) list.push({ value: 'password', label: 'Change password' });
		if (session.status?.enabled) list.push({ value: 'signout', label: 'Sign out', divider: true });
		return list;
	});

	let passwordOpen = $state(false);
	let current = $state('');
	let fresh = $state('');
	let busy = $state(false);
	let error = $state('');

	function openPassword() {
		current = '';
		fresh = '';
		error = '';
		passwordOpen = true;
	}

	// Changes the password, which ends the session
	async function changePassword() {
		busy = true;
		error = '';
		try {
			await rpc.auth.setPassword({ username: session.user?.name ?? '', currentPassword: current, password: fresh });
			passwordOpen = false;
			toast.success('Password changed', 'Sign in again');
			await session.load();
		} catch (err) {
			error = reason(err);
		} finally {
			busy = false;
		}
	}

	function onSelect(item: NeoMenuItem) {
		switch (item.value) {
			case 'theme':
				theme.update({ theme: theme.theme === 'dark' ? 'light' : 'dark' });
				return;
			case 'tokens':
				void goto('/settings/tokens');
				return;
			case 'password':
				openPassword();
				return;
			case 'signout':
				void session.logout();
				return;
		}
	}
</script>

<NeoMenu {items} onSelect={onSelect} placement="bottom-end">
	<NeoButton text aria-label="Account">
		{#snippet icon()}<UserIcon size={20} />{/snippet}
	</NeoButton>
</NeoMenu>

{#if ownPassword}
	<Dialog bind:open={passwordOpen} title="Change password" confirm="Change" {busy} disabled={!current || !fresh} onconfirm={changePassword}>
		<Secret bind:value={current} label="Current password" autocomplete="current-password" />
		<Secret bind:value={fresh} label="New password" autocomplete="new-password" />
		{#snippet footer()}
			{#if error}<span class="error-text">{error}</span>{/if}
		{/snippet}
	</Dialog>
{/if}
