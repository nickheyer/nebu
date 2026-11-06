<script lang="ts">
	import { clone } from '@bufbuild/protobuf';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoDialog } from '#lib/neo/floating/dialog/index.js';
	import { NeoPill } from '#lib/neo/pill/index.js';
	import { useNeoThemeContext } from '#lib/neo/providers/neo-theme-provider-context.svelte.js';
	import { NeoSource, NeoTheme, NeoTransition } from '#lib/neo/providers/neo-theme-provider.model.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { ago, capitalize, enumLabel, ms, plural, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { Role, RoleSchema, Scope, ScopeSchema, type ApiToken, type User } from '#lib/proto/nebu/v1/auth_pb.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import type { GatewayStatus } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { SettingsSchema, ToolServerKind, ToolServerKindSchema, type ToolServer } from '#lib/proto/nebu/v1/settings_pb.js';
	import { SourceKindSchema, type Provider, type SourceStatus } from '#lib/proto/nebu/v1/source_pb.js';
	import type { ToolInfo, ToolPart, ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import Dot from '#lib/ui/Dot.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import type { MenuItem } from '#lib/ui/Menu.svelte';
	import NameConfirm from '#lib/ui/NameConfirm.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import Code from '#lib/ui/fields/Code.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import Split from '#lib/ui/layout/Split.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { toolLamp, type Lamp } from '#lib/ui/status.js';
	import SourceEditor from '#lib/pages/settings/SourceEditor.svelte';
	import ToolEditor from '#lib/pages/settings/ToolEditor.svelte';
	import CheckIcon from 'phosphor-svelte/lib/CheckIcon';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import XIcon from 'phosphor-svelte/lib/XIcon';

	let { tab = 'general' }: { tab?: string } = $props();

	const admin = $derived(session.admin);
	const tabs = $derived(
		admin
			? [
					{ id: 'general', label: 'General' },
					{ id: 'sources', label: 'Sources' },
					{ id: 'tools', label: 'Tools' },
					{ id: 'accounts', label: 'Accounts' },
					{ id: 'tokens', label: 'API tokens' }
				]
			: [{ id: 'tokens', label: 'API tokens' }]
	);
	const active = $derived(tabs.some((t) => t.id === tab) ? tab : (tabs[0]?.id ?? 'tokens'));

	// General
	const theme = useNeoThemeContext();
	let hostLabel = $state('');
	let labelSaving = $state(false);
	let labelError = $state<string | undefined>(undefined);
	let gateway = $state<GatewayStatus | undefined>(undefined);
	let gatewayError = $state('');
	let gatewayAsked = $state(false);
	const labelDirty = $derived(hostLabel !== (live.settings?.hostLabel ?? ''));
	let themeChoice = $state<'light' | 'dark' | 'system'>(theme.remember ? theme.theme : 'system');

	$effect(() => {
		hostLabel = live.settings?.hostLabel ?? '';
	});

	$effect(() => {
		if (active !== 'general' || gatewayAsked) return;
		gatewayAsked = true;
		void rpc.gateway
			.getGatewayStatus({})
			.then((r) => (gateway = r.status))
			.catch((err) => {
				gatewayError = reason(err);
				toast.error('Daemon status failed to load', gatewayError);
			});
	});

	async function saveLabel() {
		labelSaving = true;
		labelError = undefined;
		try {
			const base = live.settings ?? (await rpc.settings.getSettings({})).settings;
			if (!base) throw new Error('the daemon answered without settings');
			const settings = clone(SettingsSchema, base);
			settings.hostLabel = hostLabel.trim();
			await rpc.settings.updateSettings({ settings });
			toast.success('Host label saved');
		} catch (err) {
			labelError = reason(err);
		} finally {
			labelSaving = false;
		}
	}

	function setTheme(v: 'light' | 'dark' | 'system') {
		if (v === 'system') {
			theme.update({ remember: false, theme: window.matchMedia('(prefers-color-scheme: dark)').matches ? NeoTheme.Dark : NeoTheme.Light });
			return;
		}
		theme.update({ remember: true, theme: v });
	}

	// Sources
	const sources = new ListQuery<SourceStatus, Record<string, never>>(
		async (_p, cursor, limit, signal) => {
			const res = await rpc.sources.listSources({ cursor, limit }, { signal });
			return { items: res.sources, next: res.nextCursor };
		},
		[EventKind.SOURCE]
	);
	let providers = $state<Provider[]>([]);
	let providersAsked = $state(false);
	let sourceId = $state<string | undefined>(undefined);
	let sourceCreating = $state(false);

	$effect(() => {
		if (active !== 'sources' || !admin) return;
		void sources.load({});
	});

	$effect(() => {
		if (active !== 'sources' || !admin || providersAsked) return;
		providersAsked = true;
		void rpc.sources
			.listProviders({})
			.then((r) => (providers = r.providers))
			.catch((err) => toast.error('Providers failed to list', reason(err)));
	});

	async function deleteSource(id: string) {
		try {
			await rpc.sources.deleteSource({ id });
			toast.success(`Deleted ${id}`);
			if (sourceId === id) sourceId = undefined;
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}

	function sourceLamp(s: SourceStatus): Lamp {
		if (s.error) return 'error';
		if (s.capabilities?.authRequired && !s.capabilities.tokenPresent) return 'warning';
		return 'success';
	}

	function sourceWord(s: SourceStatus): string {
		const c = s.capabilities;
		if (s.error) return 'failed';
		if (c?.authRequired && !c.tokenPresent) return 'token missing';
		if (c?.tokenPresent) return 'token set';
		return c?.endpoint ?? '';
	}

	function providerName(s: SourceStatus): string {
		const kind = s.source?.kind ?? 0;
		return s.capabilities?.name || providers.find((p) => p.kind === kind)?.name || enumLabel(SourceKindSchema, kind);
	}

	function capabilities(s: SourceStatus): string {
		const c = s.capabilities;
		if (!c) return '';
		const on = [c.browse && 'browse', c.search && 'search', c.paginate && 'paginate', c.card && 'card', c.revisions && 'revisions'].filter(Boolean);
		return on.length ? on.join(', ') : 'no capabilities';
	}

	function editSource(id: string | undefined) {
		sourceId = id;
		sourceCreating = false;
	}

	// Tools
	const toolServers = new ListQuery<ToolServerStatus, Record<string, never>>(
		async (_p, cursor, limit, signal) => {
			const res = await rpc.tools.listToolServers({ mesh: true, cursor, limit }, { signal });
			return { items: res.servers, next: res.nextCursor };
		},
		[EventKind.TOOL_SERVER, EventKind.SETTINGS]
	);
	// This node's servers as the live stream last reported them, members' as listed
	const toolRows = $derived(toolServers.items.map((st) => (st.nodeId ? st : (live.toolServers.get(st.server?.name ?? '') ?? st))));
	let toolEditing = $state<ToolServer | undefined>(undefined);
	let toolOpen = $state(false);
	// Bumped when a toggle fails so the switch returns to the stored state
	let toggleFailures = $state(0);

	$effect(() => {
		if (active !== 'tools' || !admin) return;
		void toolServers.load({});
	});

	// Writes the settings back with one stored tool server changed, refusing when it is gone
	async function writeTool(t: ToolServer, change: (list: ToolServer[]) => ToolServer[]) {
		const base = live.settings ?? (await rpc.settings.getSettings({})).settings;
		if (!base) throw new Error('the daemon answered without settings');
		if (!base.toolServers.some((x) => x.id === t.id)) throw new Error(`${t.name} was deleted`);
		const settings = clone(SettingsSchema, base);
		settings.toolServers = change(settings.toolServers);
		await rpc.settings.updateSettings({ settings });
	}

	async function toggleTool(t: ToolServer, on: boolean) {
		try {
			await writeTool(t, (list) => list.map((x) => (x.id === t.id ? { ...x, enabled: on } : x)));
		} catch (err) {
			toggleFailures++;
			toast.error(`${t.name} failed to ${on ? 'enable' : 'disable'}`, reason(err));
		}
	}

	async function deleteTool(t: ToolServer) {
		try {
			await writeTool(t, (list) => list.filter((x) => x.id !== t.id));
			toast.success(`Deleted ${t.name}`);
			if (toolEditing?.id === t.id) closeEditor();
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}

	function editTool(t: ToolServer | undefined) {
		toolEditing = t;
		toolOpen = true;
	}

	function toolMenu(st: ToolServerStatus): MenuItem[] {
		const t = st.server;
		const trial: MenuItem = { label: 'Try', onclick: () => openTry(st), disabled: st.tools.length === 0 };
		if (!t || st.nodeId) return [trial];
		return [{ label: 'Edit', onclick: () => editTool(t) }, { label: 'Delete', danger: true, confirm: `Delete ${t.name}?`, onclick: () => deleteTool(t) }, trial];
	}

	// The one word that tells servers of a kind apart, the member, provider, command, host or local reads
	function summaryWord(t: ToolServer, st: ToolServerStatus): string {
		if (st.nodeId) return live.nodes.get(st.nodeId)?.name ?? st.nodeId;
		switch (t.kind) {
			case ToolServerKind.SEARCH:
				return t.provider;
			case ToolServerKind.STDIO: {
				const program = t.command.trim().split(/\s+/)[0] ?? '';
				return program.split(/[\\/]/).pop() ?? program;
			}
			case ToolServerKind.HTTP:
				return t.url.replace(/^[a-z][a-z0-9+.-]*:\/\//i, '').split('/')[0] ?? t.url;
			default:
				return t.local ? 'local' : 'web';
		}
	}

	function clip(s: string, n: number): string {
		return s.length > n ? `${s.slice(0, n - 1)}…` : s;
	}

	// Try
	let tryOpen = $state(false);
	let tryServer = $state<ToolServerStatus | undefined>(undefined);
	let tryTool = $state<string | undefined>(undefined);
	let tryArgs = $state('{}');
	let tryValid = $state(true);
	let trySeedError = $state('');
	let tryBusy = $state(false);
	let tryResult = $state<{ parts: ToolPart[]; failed: boolean; tookMs: number } | undefined>(undefined);

	function openTry(st: ToolServerStatus) {
		tryServer = st;
		tryTool = st.tools[0]?.name;
		tryResult = undefined;
		tryOpen = true;
	}

	$effect(() => {
		const seeded = seedArgs(tryServer?.tools.find((x) => x.name === tryTool));
		tryArgs = seeded.text;
		trySeedError = seeded.error;
		tryResult = undefined;
	});

	// A JSON object with every schema property set to its default or an empty value of its type
	function seedArgs(t: ToolInfo | undefined): { text: string; error: string } {
		if (!t || !t.schema.trim()) return { text: '{}', error: '' };
		let schema: { properties?: Record<string, { type?: string; default?: unknown }> };
		try {
			schema = JSON.parse(t.schema) as typeof schema;
		} catch (err) {
			return { text: '{}', error: `Schema unreadable ${reason(err)}` };
		}
		const out: Record<string, unknown> = {};
		for (const [k, v] of Object.entries(schema.properties ?? {})) {
			out[k] = v.default ?? (v.type === 'number' || v.type === 'integer' ? 0 : v.type === 'boolean' ? false : v.type === 'array' ? [] : v.type === 'object' ? {} : '');
		}
		return { text: JSON.stringify(out, null, 2), error: '' };
	}

	async function runTool() {
		if (!tryTool) return;
		tryBusy = true;
		try {
			const res = await rpc.tools.callTool({ name: tryTool, arguments: tryArgs, nodeId: tryServer?.nodeId ?? '' });
			tryResult = { parts: res.parts, failed: res.failed, tookMs: res.tookMs };
		} catch (err) {
			toast.error('Call failed', reason(err));
		} finally {
			tryBusy = false;
		}
	}

	// Accounts
	const accountsOff = $derived(!session.status?.enabled || session.status.sso !== null);
	const users = new ListQuery<User, Record<string, never>>(async (_p, cursor, limit, signal) => {
		const res = await rpc.auth.listUsers({ cursor, limit }, { signal });
		return { items: res.users, next: res.nextCursor };
	}, []);
	let newUser = $state('');
	let newPassword = $state('');
	let newRole = $state<Role>(Role.OPERATOR);
	let userBusy = $state(false);
	let passwordFor = $state<User | undefined>(undefined);
	let passwordNew = $state('');
	let passwordOpen = $state(false);
	let deletingUser = $state<User | undefined>(undefined);
	let deleteUserOpen = $state(false);

	$effect(() => {
		if (active === 'accounts' && admin && !accountsOff) void users.load({});
	});

	async function createUser() {
		userBusy = true;
		try {
			await rpc.auth.createUser({ username: newUser.trim(), password: newPassword, role: newRole });
			toast.success(`Added ${newUser.trim()}`);
			newUser = '';
			newPassword = '';
			await users.load({});
		} catch (err) {
			toast.error('Add failed', reason(err));
		} finally {
			userBusy = false;
		}
	}

	async function setPassword() {
		if (!passwordFor) return;
		userBusy = true;
		try {
			await rpc.auth.setPassword({ username: passwordFor.username, password: passwordNew });
			toast.success(`Password set for ${passwordFor.username}`);
			passwordOpen = false;
			passwordNew = '';
		} catch (err) {
			toast.error('Password failed', reason(err));
		} finally {
			userBusy = false;
		}
	}

	async function deleteUser() {
		if (!deletingUser) return;
		const name = deletingUser.username;
		try {
			await rpc.auth.deleteUser({ username: name });
			toast.success(`Deleted ${name}`);
			await users.load({});
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}

	const roles = [
		{ value: Role.ADMIN, label: 'Admin' },
		{ value: Role.OPERATOR, label: 'Operator' },
		{ value: Role.INFERENCE, label: 'Inference' }
	];

	// Tokens
	const tokens = new ListQuery<ApiToken, Record<string, never>>(async (_p, cursor, limit, signal) => {
		const res = await rpc.auth.listTokens({ cursor, limit }, { signal });
		return { items: res.tokens, next: res.nextCursor };
	}, []);
	let tokenName = $state('');
	let tokenScopes = $state<Scope[]>([Scope.INFERENCE]);
	let tokenBusy = $state(false);
	let madeToken = $state('');
	let currentPassword = $state('');
	let ownPassword = $state('');
	let passwordBusy = $state(false);
	const roleScopes = $derived(session.role === 'admin' ? [Scope.INFERENCE, Scope.READ, Scope.OPERATE, Scope.ADMIN] : session.role === 'operator' ? [Scope.INFERENCE, Scope.READ, Scope.OPERATE] : [Scope.INFERENCE]);

	$effect(() => {
		if (active === 'tokens' && session.status?.enabled) void tokens.load({});
	});

	async function createToken() {
		tokenBusy = true;
		try {
			const res = await rpc.auth.createToken({ name: tokenName.trim(), scopes: tokenScopes });
			if (!res.token?.secret) throw new Error('the daemon answered without the secret');
			madeToken = res.token.secret;
			tokenName = '';
			await tokens.load({});
		} catch (err) {
			toast.error('Create failed', reason(err));
		} finally {
			tokenBusy = false;
		}
	}

	async function revoke(t: ApiToken) {
		try {
			await rpc.auth.deleteToken({ id: t.id });
			toast.success(`Revoked ${t.name}`);
			await tokens.load({});
		} catch (err) {
			toast.error('Revoke failed', reason(err));
		}
	}

	async function changeOwnPassword() {
		passwordBusy = true;
		try {
			await rpc.auth.setPassword({ username: session.user?.name ?? '', currentPassword, password: ownPassword });
			toast.success('Password changed', 'Sign in again');
			currentPassword = '';
			ownPassword = '';
		} catch (err) {
			toast.error('Password failed', reason(err));
		} finally {
			passwordBusy = false;
		}
	}

	function scopeLabel(s: Scope): string {
		return enumLabel(ScopeSchema, s);
	}

	const sourceEditorOpen = $derived(sourceId !== undefined || sourceCreating);

	function closeEditor() {
		sourceId = undefined;
		sourceCreating = false;
		toolOpen = false;
		toolEditing = undefined;
	}
</script>

{#snippet daemonSection()}
	<FormSection title="Daemon">
		{#if !gateway && !gatewayError}
			<Skeleton rows={3} height={32} />
		{:else}
			<KeyRows
				rows={[
					{ label: 'version', value: gateway ? `${gateway.implementation} ${gateway.version}`.trim() : '' },
					{ label: 'listener', value: gateway?.listeners.map((l) => l.addr).join(', ') ?? '', mono: true, copy: true },
					{ label: 'home', value: live.host?.home ?? '', mono: true, copy: true }
				]}
			/>
			{#if gatewayError}<p class="error-text secondary">{gatewayError}</p>{/if}
		{/if}
	</FormSection>
{/snippet}

{#snippet generalForms()}
	<Stack gap={24}>
		<FormSection title="Host">
			<Text bind:value={hostLabel} label="Host label" max={64} placeholder={live.host?.hostname} />
		</FormSection>
		<FormSection title="Appearance" info="Remembered in this browser">
			<FieldRow>
				<Segmented bind:value={themeChoice} options={[{ value: 'light', label: 'Light' }, { value: 'dark', label: 'Dark' }, { value: 'system', label: 'System' }]} label="Theme" onchange={setTheme} />
				<Select value={theme.source} options={Object.values(NeoSource).map((s) => ({ value: s, label: capitalize(s.replaceAll('-', ' ')) }))} label="Light source" onchange={(v) => v && theme.update({ source: v })} />
			</FieldRow>
			<FieldRow>
				<Switch checked={theme.transition !== NeoTransition.None} label="Transitions" onchange={(on) => theme.update({ transition: on ? NeoTransition.Circle : NeoTransition.None })} />
				<Switch checked={theme.reset} label="Style reset" onchange={(on) => theme.update({ reset: on })} />
			</FieldRow>
		</FormSection>
		<SaveBar dirty={labelDirty} saving={labelSaving} error={labelError} onsave={saveLabel} ondiscard={() => (hostLabel = live.settings?.hostLabel ?? '')} />
	</Stack>
{/snippet}

{#snippet generalTab()}
	{#if viewport.ultrawide}
		<Split ratio="3/5">
			{#snippet left()}{@render generalForms()}{/snippet}
			{#snippet right()}{@render daemonSection()}{/snippet}
		</Split>
	{:else}
		<Stack gap={24}>
			{@render generalForms()}
			{@render daemonSection()}
		</Stack>
	{/if}
{/snippet}

{#snippet sourcesList()}
	<Stack gap={8}>
		<Row gap={12}>
			<Cell grow={false}>
				<NeoButton elevation={2} onclick={() => { sourceCreating = true; sourceId = undefined; }}>{#snippet icon()}<PlusIcon size={20} />{/snippet}Add source</NeoButton>
			</Cell>
		</Row>
		{#if !sources.settled}
			<Skeleton rows={4} />
		{:else if sources.error}
			<Empty text={sources.error} action="Retry" onclick={() => sources.refresh()} />
		{:else if sources.items.length === 0}
			<Empty text="No sources" action="Add source" onclick={() => (sourceCreating = true)} />
		{:else}
			<div class="rows">
				{#each sources.items as s (s.source?.id)}
					{@const src = s.source}
					{@const lamp = sourceLamp(s)}
					<ListRow
						primary={src?.id ?? ''}
						secondary={sourceWord(s)}
						secondaryWidth="40ch"
						selected={sourceId === src?.id}
						onclick={() => editSource(src?.id)}
						menu={src?.seeded ? undefined : [{ label: 'Delete', danger: true, confirm: `Delete ${src?.id}?`, onclick: () => deleteSource(src?.id ?? '') }]}
					>
						{#snippet leading()}
							<Tip text={s.error || (lamp === 'warning' ? 'token missing' : undefined)} tag="span"><Dot {lamp} /></Tip>
						{/snippet}
						{#snippet badges()}
							<Tip text={capabilities(s) || undefined} tag="span"><span class="provider"><NeoPill size="small" text tinted label={providerName(s)} /></span></Tip>
						{/snippet}
						{#snippet action()}<NeoButton text onclick={() => editSource(src?.id)}>Edit</NeoButton>{/snippet}
					</ListRow>
				{/each}
			</div>
			<LoadMore next={sources.next} loading={sources.loading} onmore={() => sources.more()} />
		{/if}
	</Stack>
{/snippet}

{#snippet sourceDetail()}
	{#if sourceCreating || sourceId}
		{#key sourceId ?? 'new'}
			<SourceEditor status={sources.items.find((s) => s.source?.id === sourceId)} {providers} onclose={closeEditor} />
		{/key}
	{/if}
{/snippet}

{#snippet toolsList()}
	<Stack gap={8}>
		<Row gap={12}>
			<Cell grow={false}>
				<NeoButton elevation={2} onclick={() => editTool(undefined)}>{#snippet icon()}<PlusIcon size={20} />{/snippet}Add tool server</NeoButton>
			</Cell>
		</Row>
		{#if !toolServers.settled}
			<Skeleton rows={3} />
		{:else if toolServers.error}
			<Empty text={toolServers.error} action="Retry" onclick={() => toolServers.refresh()} />
		{:else if toolRows.length === 0}
			<Empty text="No tool servers" action="Add tool server" onclick={() => editTool(undefined)} />
		{:else}
			<div class="rows">
				{#each toolRows as st (st.ref || st.server?.name)}
					{@const t = st.server}
					{#if t}
						{@const word = summaryWord(t, st)}
						<ListRow
							primary={t.name}
							primaryTip={st.ref && st.ref !== t.name ? st.ref : undefined}
							secondary={enumLabel(ToolServerKindSchema, t.kind)}
							secondaryWidth="8ch"
							facts={[{ value: clip(word, 16), tip: word.length > 16 ? word : undefined }, { value: plural(st.tools.length, 'tool') }]}
							selected={toolOpen && toolEditing?.id === t.id && !st.nodeId}
							menu={toolMenu(st)}
						>
							{#snippet leading()}
								<Tip text={st.error || undefined} tag="span"><Dot lamp={toolLamp(st.state)} /></Tip>
							{/snippet}
							{#snippet action()}
								{#if st.nodeId}
									<span class="secondary readonly">read only</span>
								{:else}
									{#key `${t.enabled}:${toggleFailures}`}<Switch checked={t.enabled} onchange={(on) => toggleTool(t, on)} />{/key}
								{/if}
							{/snippet}
							{#snippet expand()}
								{#each st.tools as tool (tool.name)}
									<Tip text={tool.schema || undefined} tag="div" class="tooltip-line">
										<div class="toolline">
											<span class="toolname"><Str value={tool.name} weight={500} width="100%" /></span>
											<span class="toolmark">{#if tool.readOnly}<StatePill label="read only" />{/if}</span>
											<Str value={tool.description} kind="message" color="muted" />
										</div>
									</Tip>
								{:else}
									<span class="secondary">No tools</span>
								{/each}
							{/snippet}
						</ListRow>
					{/if}
				{/each}
			</div>
			<LoadMore next={toolServers.next} loading={toolServers.loading} onmore={() => toolServers.more()} />
		{/if}
	</Stack>
{/snippet}

{#snippet toolDetail()}
	{#if toolOpen}
		{#key toolEditing?.id ?? 'new'}
			<ToolEditor server={toolEditing} onclose={closeEditor} />
		{/key}
	{/if}
{/snippet}

{#snippet accountsTab()}
	{#if accountsOff}
		<Empty text={session.status?.sso ? `Accounts come from ${session.status.sso.name}` : 'Authentication is off'} />
	{:else}
		<Stack gap={24}>
			{#if !users.settled}
				<Skeleton rows={3} />
			{:else if users.error}
				<Empty text={users.error} action="Retry" onclick={() => users.refresh()} />
			{:else}
				<div class="rows">
					{#each users.items as u (u.id)}
						<ListRow
							primary={u.username}
							facts={[{ value: ago(u.createdAt, clockTick.now), tip: `created ${when(u.createdAt)}` }]}
							menu={[
								{ label: 'Set password', onclick: () => { passwordFor = u; passwordNew = ''; passwordOpen = true; } },
								{ label: 'Delete', danger: true, divider: true, onclick: () => { deletingUser = u; deleteUserOpen = true; } }
							]}
						>
							{#snippet badges()}<StatePill label={enumLabel(RoleSchema, u.role)} lamp={u.role === Role.ADMIN ? 'primary' : 'muted'} />{/snippet}
						</ListRow>
					{:else}
						<Empty text="No accounts" />
					{/each}
				</div>
				<LoadMore next={users.next} loading={users.loading} onmore={() => users.more()} />
			{/if}
			<FormSection title="Add account">
				<FieldRow>
					<Text bind:value={newUser} label="Username" min={1} max={64} autocomplete="off" />
					<Secret bind:value={newPassword} label="Password" autocomplete="new-password" />
				</FieldRow>
				<Segmented bind:value={newRole} options={roles} label="Role" />
				<Row gap={12} justify="flex-end">
					<NeoButton class="primary" elevation={2} loading={userBusy} disabled={!newUser.trim() || !newPassword} onclick={createUser}>Add</NeoButton>
				</Row>
			</FormSection>
		</Stack>
	{/if}
{/snippet}

{#snippet tokensTab()}
	{#if !session.status?.enabled}
		<Empty text="Authentication is off" />
	{:else}
		<Stack gap={24}>
			{#if !tokens.settled}
				<Skeleton rows={3} />
			{:else if tokens.error}
				<Empty text={tokens.error} action="Retry" onclick={() => tokens.refresh()} />
			{:else}
				<div class="rows">
					{#each tokens.items as t (t.id)}
						<ListRow
							primary={t.name}
							secondary={t.prefix}
							secondaryKind="id"
							secondaryWidth="14ch"
							facts={[t.lastUsedAt ? { value: ago(t.lastUsedAt, clockTick.now), tip: `last used ${when(t.lastUsedAt)}` } : { value: 'never used', tip: `created ${when(t.createdAt)}` }]}
							menu={[{ label: 'Revoke', danger: true, confirm: `Revoke ${t.name}?`, onclick: () => revoke(t) }]}
						>
							{#snippet badges()}
								<Tip text={t.scopes.length > 2 ? t.scopes.map(scopeLabel).join(', ') : undefined} tag="span" class="scopes">
									{#each t.scopes.slice(0, 2) as s (s)}<StatePill label={scopeLabel(s)} lamp={s === Scope.ADMIN ? 'primary' : 'muted'} />{/each}
									{#if t.scopes.length > 2}<StatePill label="+{t.scopes.length - 2}" />{/if}
								</Tip>
							{/snippet}
						</ListRow>
					{:else}
						<Empty text="No tokens" />
					{/each}
				</div>
				<LoadMore next={tokens.next} loading={tokens.loading} onmore={() => tokens.more()} />
			{/if}
			<FormSection title="Create token">
				<FieldRow>
					<Text bind:value={tokenName} label="Name" min={1} max={64} />
					<Checklist bind:value={tokenScopes} options={roleScopes.map((s) => ({ value: s, label: capitalize(scopeLabel(s)) }))} label="Scopes" />
				</FieldRow>
				<Row gap={12} justify="flex-end">
					<NeoButton class="primary" elevation={2} loading={tokenBusy} disabled={!tokenName.trim() || tokenScopes.length === 0} onclick={createToken}>Create</NeoButton>
				</Row>
			</FormSection>
			{#if session.status.local}
				<FormSection title="Change my password">
					<FieldRow>
						<Secret bind:value={currentPassword} label="Current password" autocomplete="current-password" />
						<Secret bind:value={ownPassword} label="New password" autocomplete="new-password" />
					</FieldRow>
					<Row gap={12} justify="flex-end">
						<NeoButton elevation={2} loading={passwordBusy} disabled={!currentPassword || !ownPassword} onclick={changeOwnPassword}>Change</NeoButton>
					</Row>
				</FormSection>
			{/if}
		</Stack>
	{/if}
{/snippet}

<Stack gap={16}>
	<PageTabs {tabs} {active} base="/settings" />
	{#if active === 'general'}
		{@render generalTab()}
	{:else if active === 'sources'}
		<Sheet open={sourceEditorOpen} onclose={closeEditor} list={sourcesList} empty="Select a source">{@render sourceDetail()}</Sheet>
	{:else if active === 'tools'}
		<Sheet open={toolOpen} onclose={closeEditor} list={toolsList} empty="Select a tool server">{@render toolDetail()}</Sheet>
	{:else if active === 'accounts'}
		{@render accountsTab()}
	{:else}
		{@render tokensTab()}
	{/if}
</Stack>

<NeoDialog bind:open={tryOpen} modal rounded="md" filled elevation={2} width="min(40rem, 92vw)" padding="16px">
	<Stack gap={12}>
		<h3>Try {tryServer?.server?.name}</h3>
		<Select bind:value={tryTool} options={(tryServer?.tools ?? []).map((t) => ({ value: t.name, label: t.name, description: t.description }))} label="Tool" />
		<Code bind:value={tryArgs} bind:valid={tryValid} language="json" rows={6} label="Arguments" />
		{#if trySeedError}<p class="error-text secondary">{trySeedError}</p>{/if}
		<Row justify="flex-end" gap={12}>
			<NeoButton text onclick={() => (tryOpen = false)}>Close</NeoButton>
			<NeoButton class="primary" elevation={2} loading={tryBusy} disabled={!tryTool || !tryValid} onclick={runTool}>Run</NeoButton>
		</Row>
		{#if tryResult}
			<div class="result">
				<span class="mark" class:bad={tryResult.failed} role="img" aria-label={tryResult.failed ? 'failed' : 'succeeded'}>
					{#if tryResult.failed}<XIcon size={20} />{:else}<CheckIcon size={20} />{/if}
				</span>
				<span class="secondary">{ms(tryResult.tookMs)}</span>
				<span class="secondary">{plural(tryResult.parts.length, 'part')}</span>
			</div>
			{#each tryResult.parts as p, i (i)}
				{#if p.type === 'image'}
					<img class="partimg" src="data:{p.mediaType};base64,{p.data}" alt="Part {i + 1}" />
				{:else}
					<pre class="screen part">{p.text}</pre>
				{/if}
			{/each}
		{/if}
	</Stack>
</NeoDialog>
<NeoDialog bind:open={passwordOpen} modal rounded="md" filled elevation={2} width="min(28rem, 92vw)" padding="16px">
	<Stack gap={12}>
		<h3>Set password for {passwordFor?.username}</h3>
		<Secret bind:value={passwordNew} label="New password" autocomplete="new-password" />
		<Row justify="flex-end" gap={12}>
			<NeoButton text onclick={() => (passwordOpen = false)}>Cancel</NeoButton>
			<NeoButton class="primary" elevation={2} loading={userBusy} disabled={!passwordNew} onclick={setPassword}>Set</NeoButton>
		</Row>
	</Stack>
</NeoDialog>
<NeoDialog open={madeToken !== ''} modal rounded="md" filled elevation={2} width="min(32rem, 92vw)" padding="16px" onclose={() => (madeToken = '')}>
	<Stack gap={12}>
		<h3>Token created</h3>
		<span class="secondary">Shown once</span>
		<div class="token"><code class="screen secret">{madeToken}</code><CopyButton text={madeToken} /></div>
		<Row gap={12} justify="flex-end"><NeoButton class="primary" elevation={2} onclick={() => (madeToken = '')}>Done</NeoButton></Row>
	</Stack>
</NeoDialog>
{#if deletingUser}<NameConfirm bind:open={deleteUserOpen} title="Delete account" name={deletingUser.username} message="Its sessions end" onconfirm={deleteUser} />{/if}

<style>
	.rows {
		display: flex;
		flex-direction: column;
	}
	.provider {
		display: inline-flex;
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
	.readonly {
		white-space: nowrap;
	}
	:global(.tooltip-line) {
		display: flex;
		width: 100%;
	}
	:global(.tip.scopes) {
		gap: 4px;
		align-items: center;
	}
	.toolline {
		display: flex;
		flex: 1 1 auto;
		gap: 12px;
		align-items: center;
		min-width: 0;
		min-height: 28px;
		font-size: 13px;
	}
	.toolname {
		display: flex;
		flex: 0 0 24ch;
		min-width: 0;
	}
	.toolmark {
		display: flex;
		flex: 0 0 10ch;
	}
	.result {
		display: flex;
		gap: 12px;
		align-items: center;
	}
	.mark {
		display: inline-flex;
		color: var(--neo-color-success);
	}
	.mark.bad {
		color: var(--neo-color-error);
	}
	.part {
		max-height: 40vh;
		margin: 0;
		padding: 12px;
		overflow: auto;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.partimg {
		max-width: 100%;
		border-radius: 8px;
	}
	.token {
		display: flex;
		gap: 8px;
		align-items: center;
		min-width: 0;
	}
	.secret {
		flex: 1 1 auto;
		min-width: 0;
		padding: 8px 12px;
		overflow-wrap: anywhere;
	}
	h3 {
		margin: 0;
	}
</style>
