<script lang="ts">
	import { clone } from '@bufbuild/protobuf';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import type { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { enumLabel, plural } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { SettingsSchema, ToolServerKind, ToolServerKindSchema, type ToolServer } from '#lib/proto/nebu/v1/settings_pb.js';
	import type { ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
	import type { HeaderTab } from '#lib/shell/chrome.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Dot from '#lib/ui/Dot.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import type { MenuItem } from '#lib/ui/Menu.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import { toolLamp } from '#lib/ui/status.js';
	import ToolEditor from '#lib/pages/settings/ToolEditor.svelte';
	import TryTool from '#lib/pages/settings/TryTool.svelte';
	import WrenchIcon from 'phosphor-svelte/lib/WrenchIcon';

	let { tabs, servers }: { tabs: HeaderTab[]; servers: ListQuery<ToolServerStatus, Record<string, never>> } = $props();

	// This node's servers as the live stream last reported them, members' as listed
	const rows = $derived(servers.items.map((st) => (st.nodeId ? st : (live.toolServers.get(st.server?.name ?? '') ?? st))));
	let open = $state(false);
	let editing = $state<ToolServer | undefined>(undefined);
	let trying = $state<ToolServerStatus | undefined>(undefined);
	let tryOpen = $state(false);
	// Bumped when a toggle fails so the switch returns to the stored state
	let toggleFailures = $state(0);

	function edit(t: ToolServer | undefined) {
		editing = t;
		open = true;
	}

	function close() {
		open = false;
		editing = undefined;
	}

	// Writes the settings back with one stored tool server changed, refusing when it is gone
	async function write(t: ToolServer, change: (list: ToolServer[]) => ToolServer[]) {
		const base = live.settings ?? (await rpc.settings.getSettings({})).settings;
		if (!base) throw new Error('the daemon answered without settings');
		if (!base.toolServers.some((x) => x.id === t.id)) throw new Error(`${t.name} was deleted`);
		const settings = clone(SettingsSchema, base);
		settings.toolServers = change(settings.toolServers);
		await rpc.settings.updateSettings({ settings });
	}

	async function toggle(t: ToolServer, on: boolean) {
		try {
			await write(t, (list) => list.map((x) => (x.id === t.id ? { ...x, enabled: on } : x)));
		} catch (err) {
			toggleFailures++;
			toast.error(`${t.name} failed to ${on ? 'enable' : 'disable'}`, reason(err));
		}
	}

	async function remove(t: ToolServer) {
		try {
			await write(t, (list) => list.filter((x) => x.id !== t.id));
			toast.success(`Deleted ${t.name}`);
			if (editing?.id === t.id) close();
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}

	function tryServer(st: ToolServerStatus) {
		trying = st;
		tryOpen = true;
	}

	function menuOf(st: ToolServerStatus): MenuItem[] {
		const t = st.server;
		const trial: MenuItem = { label: 'Try', onclick: () => tryServer(st), disabled: st.tools.length === 0 };
		if (!t || st.nodeId) return [trial];
		return [trial, { label: 'Delete', danger: true, divider: true, confirm: `Delete ${t.name}?`, onclick: () => remove(t) }];
	}

	function stateOf(st: ToolServerStatus): string {
		const lamp = toolLamp(st.state);
		return lamp === 'success' ? 'ready' : lamp === 'warning' ? 'connecting' : lamp === 'error' ? 'failed' : 'off';
	}

	// The one word that tells servers of a kind apart, the member, provider, command, host or local reads
	function targetOf(t: ToolServer, st: ToolServerStatus): string {
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
</script>

{#snippet list()}
	<Card title="Tool servers" info="Search, fetch and MCP servers whose tools models call through the gateway" flush>
		{#if !servers.settled}
			<Skeleton rows={3} secondary="8ch" facts={2} />
		{:else if servers.error}
			<Empty text={servers.error} action="Retry" onclick={() => servers.refresh()} />
		{:else if rows.length === 0}
			<Empty icon={WrenchIcon} text="No tool servers" hint="Web search, page fetch and MCP servers give models tools to call while they answer" action={open ? undefined : 'Add tool server'} onclick={() => edit(undefined)} />
		{:else}
			<List head={{ primary: 'Server', secondary: 'Kind', facts: ['Target', 'Tools'], action: 'Enabled' }}>
				{#each rows as st (st.ref || st.server?.name)}
					{@const t = st.server}
					{#if t}
						{@const target = targetOf(t, st)}
						<ListRow
							primary={t.name}
							primaryTip={st.ref && st.ref !== t.name ? st.ref : undefined}
							secondary={enumLabel(ToolServerKindSchema, t.kind)}
							secondaryWidth="10ch"
							facts={[{ value: clip(target, 20), tip: target.length > 20 ? target : undefined }, { value: plural(st.tools.length, 'tool') }]}
							selected={open && editing?.id === t.id && !st.nodeId}
							onclick={st.nodeId ? undefined : () => edit(t)}
							menu={menuOf(st)}
						>
							{#snippet leading()}
								<Tip text={st.error || undefined} tag="span"><Dot lamp={toolLamp(st.state)} label={stateOf(st)} /></Tip>
							{/snippet}
							{#snippet action()}
								{#if st.nodeId}
									<span class="secondary nowrap">read only</span>
								{:else}
									{#key `${t.enabled}:${toggleFailures}`}<Switch checked={t.enabled} onchange={(on) => toggle(t, on)} />{/key}
								{/if}
							{/snippet}
							{#snippet expand()}
								{#each st.tools as tool (tool.name)}
									<ListRow primary={tool.name} primaryTip={tool.schema || undefined} secondary={tool.description} secondaryKind="message" secondaryWidth="64ch">
										{#snippet badges()}{#if tool.readOnly}<StatePill label="read only" />{/if}{/snippet}
									</ListRow>
								{:else}
									<Empty text="No tools" row />
								{/each}
							{/snippet}
						</ListRow>
					{/if}
				{/each}
			</List>
			<LoadMore next={servers.next} loading={servers.loading} onmore={() => servers.more()} />
		{/if}
	</Card>
{/snippet}

<PageHeader {tabs} active="tools">
	{#snippet actions()}
		<NeoButton class="primary" elevation={2} onclick={() => edit(undefined)}>Add tool server</NeoButton>
	{/snippet}
</PageHeader>

<Sheet {open} onclose={close} {list}>
	{#if open}
		{#key editing?.id ?? 'new'}
			<ToolEditor server={editing} onclose={close} />
		{/key}
	{/if}
</Sheet>

{#if trying}<TryTool bind:open={tryOpen} server={trying} />{/if}
