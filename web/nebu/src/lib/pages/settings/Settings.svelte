<script lang="ts">
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { rpc } from '#lib/api/transport.js';
	import { session } from '#lib/auth/session.svelte.js';
	import type { User } from '#lib/proto/nebu/v1/auth_pb.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import type { SourceStatus } from '#lib/proto/nebu/v1/source_pb.js';
	import type { ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
	import type { HeaderTab } from '#lib/shell/chrome.svelte.js';
	import Accounts from '#lib/pages/settings/Accounts.svelte';
	import General from '#lib/pages/settings/General.svelte';
	import Notifications from '#lib/pages/settings/Notifications.svelte';
	import Sources from '#lib/pages/settings/Sources.svelte';
	import Tools from '#lib/pages/settings/Tools.svelte';

	let { tab = 'general' }: { tab?: string } = $props();

	const auth = $derived(session.status?.enabled === true);
	const accountsOff = $derived(!auth || session.status?.sso !== null);

	const sources = new ListQuery<SourceStatus, Record<string, never>>(
		async (_p, cursor, limit, signal) => {
			const res = await rpc.sources.listSources({ cursor, limit }, { signal });
			return { items: res.sources, next: res.nextCursor };
		},
		[EventKind.SOURCE]
	);
	const toolServers = new ListQuery<ToolServerStatus, Record<string, never>>(
		async (_p, cursor, limit, signal) => {
			const res = await rpc.tools.listToolServers({ mesh: true, cursor, limit }, { signal });
			return { items: res.servers, next: res.nextCursor };
		},
		[EventKind.TOOL_SERVER, EventKind.SETTINGS]
	);
	const users = new ListQuery<User, Record<string, never>>(async (_p, cursor, limit, signal) => {
		const res = await rpc.auth.listUsers({ cursor, limit }, { signal });
		return { items: res.users, next: res.nextCursor };
	}, []);

	$effect(() => {
		void sources.load({});
		void toolServers.load({});
	});
	$effect(() => {
		if (!accountsOff) void users.load({});
	});

	const tabs: HeaderTab[] = [
		{ id: 'general', label: 'General', href: '/settings/general' },
		{ id: 'sources', label: 'Sources', href: '/settings/sources' },
		{ id: 'tools', label: 'Tools', href: '/settings/tools' },
		{ id: 'notifications', label: 'Notifications', href: '/settings/notifications' },
		{ id: 'accounts', label: 'Accounts', href: '/settings/accounts' }
	];
	const active = $derived(tabs.some((t) => t.id === tab) ? tab : 'general');
</script>

{#if active === 'general'}
	<General {tabs} />
{:else if active === 'sources'}
	<Sources {tabs} {sources} />
{:else if active === 'tools'}
	<Tools {tabs} servers={toolServers} />
{:else if active === 'notifications'}
	<Notifications {tabs} />
{:else}
	<Accounts {tabs} {users} off={accountsOff} />
{/if}
