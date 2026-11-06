<script lang="ts">
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoMenu } from '#lib/neo/floating/menu/index.js';
	import type { NeoMenuItem } from '#lib/neo/floating/menu/index.js';
	import { useNeoThemeContext } from '#lib/neo/providers/neo-theme-provider-context.svelte.js';
	import { session } from '#lib/auth/session.svelte.js';
	import UserIcon from 'phosphor-svelte/lib/UserIcon';

	const theme = useNeoThemeContext();
	const items = $derived.by((): NeoMenuItem[] => {
		const list: NeoMenuItem[] = [];
		if (session.status?.enabled && session.user) {
			list.push({ value: 'who', label: session.displayName, description: session.role, readonly: true });
		}
		list.push({ value: 'theme', label: theme.theme === 'dark' ? 'Light theme' : 'Dark theme', divider: list.length > 0 });
		list.push({ value: 'tokens', label: 'API tokens' });
		if (session.status?.enabled) list.push({ value: 'signout', label: 'Sign out', divider: true });
		return list;
	});

	function onSelect(item: NeoMenuItem) {
		switch (item.value) {
			case 'theme':
				theme.update({ theme: theme.theme === 'dark' ? 'light' : 'dark' });
				return;
			case 'tokens':
				void goto('/settings/tokens');
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
