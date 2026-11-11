<script lang="ts">
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoMenu } from '#lib/neo/floating/menu/index.js';
	import type { NeoMenuItem } from '#lib/neo/floating/menu/index.js';
	import { session } from '#lib/auth/session.svelte.js';
	import UserIcon from 'phosphor-svelte/lib/UserIcon';

	// The signed in account, which opens its profile, then the way out
	const items = $derived.by((): NeoMenuItem[] => {
		const signedIn = session.status?.enabled === true && session.user !== null;
		const list: NeoMenuItem[] = [{ value: 'profile', label: signedIn ? session.displayName : 'Profile', description: signedIn ? session.role : undefined }];
		if (session.status?.enabled) list.push({ value: 'signout', label: 'Sign out', divider: true });
		return list;
	});

	function onSelect(item: NeoMenuItem) {
		if (item.value === 'profile') void goto('/profile');
		else if (item.value === 'signout') void session.logout();
	}
</script>

<NeoMenu {items} onSelect={onSelect} placement="bottom-end">
	<NeoButton text aria-label="Account">
		{#snippet icon()}<UserIcon size={20} />{/snippet}
	</NeoButton>
</NeoMenu>
