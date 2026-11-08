<script lang="ts">
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { rpc } from '#lib/api/transport.js';
	import { session } from '#lib/auth/session.svelte.js';
	import type { ApiToken, User } from '#lib/proto/nebu/v1/auth_pb.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import type { SourceStatus } from '#lib/proto/nebu/v1/source_pb.js';
	import type { ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
	import type { HeaderTab } from '#lib/shell/chrome.svelte.js';
	import Accounts from '#lib/pages/settings/Accounts.svelte';
	import General from '#lib/pages/settings/General.svelte';
	import Sources from '#lib/pages/settings/Sources.svelte';
	import Tokens from '#lib/pages/settings/Tokens.svelte';
	import Tools from '#lib/pages/settings/Tools.svelte';

	let { tab = 'general' }: { tab?: string } = $props();

	const admin = $derived(session.admin);
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
	const tokens = new ListQuery<ApiToken, Record<string, never>>(async (_p, cursor, limit, signal) => {
		const res = await rpc.auth.listTokens({ cursor, limit }, { signal });
		return { items: res.tokens, next: res.nextCursor };
	}, []);

	$effect(() => {
		if (admin) void sources.load({});
	});
	$effect(() => {
		if (admin) void toolServers.load({});
	});
	$effect(() => {
		if (admin && !accountsOff) void users.load({});
	});
	$effect(() => {
		if (auth) void tokens.load({});
	});

	// Every list the tabs count, settled together so no tab reads a count while another loads
	const counted = $derived((!admin || (sources.settled && toolServers.settled && (accountsOff || users.settled))) && (!auth || tokens.settled));
	const tabs = $derived.by((): HeaderTab[] => {
		if (!admin) return [];
		const count = (n: number | undefined) => (counted ? n : undefined);
		return [
			{ id: 'general', label: 'General', href: '/settings/general' },
			{ id: 'sources', label: 'Sources', href: '/settings/sources', count: count(sources.items.length) },
			{ id: 'tools', label: 'Tools', href: '/settings/tools', count: count(toolServers.items.length) },
			{ id: 'accounts', label: 'Accounts', href: '/settings/accounts', count: count(accountsOff ? undefined : users.items.length) },
			{ id: 'tokens', label: 'API tokens', href: '/settings/tokens', count: count(auth ? tokens.items.length : undefined) }
		];
	});
	// Accounts below admin reach their tokens alone
	const active = $derived(!admin ? 'tokens' : tabs.some((t) => t.id === tab) ? tab : 'general');
</script>

{#if active === 'general'}
	<General {tabs} />
{:else if active === 'sources'}
	<Sources {tabs} {sources} />
{:else if active === 'tools'}
	<Tools {tabs} servers={toolServers} />
{:else if active === 'accounts'}
	<Accounts {tabs} {users} off={accountsOff} />
{:else}
	<Tokens {tabs} {tokens} />
{/if}
