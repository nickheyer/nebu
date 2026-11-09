<script lang="ts">
	import { clone, create, equals } from '@bufbuild/protobuf';
	import { untrack } from 'svelte';
	import { reason, rpc } from '#lib/api/transport.js';
	import { live } from '#lib/live/events.svelte.js';
	import { SettingsSchema, ToolServerKind, ToolServerSchema, type ToolServer } from '#lib/proto/nebu/v1/settings_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Chips from '#lib/ui/fields/Chips.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import KeyValues from '#lib/ui/fields/KeyValues.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';

	let { server, onclose }: { server?: ToolServer; onclose: () => void } = $props();

	const draft = untrack(() => server);
	const editing = draft !== undefined;
	let s = $state(draft ? clone(ToolServerSchema, draft) : create(ToolServerSchema, { enabled: true, kind: ToolServerKind.SEARCH, provider: 'searxng' }));
	const baseline = untrack(() => clone(ToolServerSchema, s));
	const dirty = $derived(!editing || !equals(ToolServerSchema, s, baseline));
	let saving = $state(false);
	let error = $state<string | undefined>(undefined);
	let generation = $state(0);

	const kinds = [
		{ value: ToolServerKind.SEARCH, label: 'search', description: 'SearXNG or Brave' },
		{ value: ToolServerKind.FETCH, label: 'fetch', description: 'Pages as text' },
		{ value: ToolServerKind.STDIO, label: 'stdio', description: 'MCP over stdio' },
		{ value: ToolServerKind.HTTP, label: 'http', description: 'MCP over HTTP' }
	];

	function pickKind(k: ToolServerKind | undefined) {
		if (k === ToolServerKind.SEARCH && !s.provider) s.provider = 'searxng';
	}

	function discard() {
		if (!editing) {
			onclose();
			return;
		}
		s = clone(ToolServerSchema, baseline);
		generation++;
	}

	// Writes the whole tool server list back with this one replaced or appended
	async function save() {
		saving = true;
		error = undefined;
		try {
			const base = live.settings ?? (await rpc.settings.getSettings({})).settings;
			if (!base) throw new Error('the daemon answered without settings');
			const settings = clone(SettingsSchema, base);
			const next = clone(ToolServerSchema, s);
			next.name = next.name.trim();
			if (editing && !settings.toolServers.some((t) => t.id === next.id)) throw new Error(`${next.name} was deleted`);
			if (editing) settings.toolServers = settings.toolServers.map((t) => (t.id === next.id ? next : t));
			else if (settings.toolServers.some((t) => t.name === next.name)) throw new Error(`a tool server named ${next.name} exists`);
			else settings.toolServers = [...settings.toolServers, next];
			await rpc.settings.updateSettings({ settings });
			toast.success(`Saved ${next.name}`);
			onclose();
		} catch (err) {
			error = reason(err);
		} finally {
			saving = false;
		}
	}
</script>

<PanelHeader title={draft ? draft.name : 'New tool server'} {onclose} />
<Stack gap={24}>
	<FormSection title="Server" info="A fetch server reaching local addresses can read this host's own services">
		<FieldRow>
			<Select bind:value={s.kind} options={kinds} label="Kind" info="Search, page fetch, or an MCP server over stdio or HTTP" disabled={editing} onchange={pickKind} />
			<Text bind:value={s.name} label="Name" info="How routes and personas attach the server, MCP tools offered as name__tool" required />
		</FieldRow>
		{#key generation}
			<FieldRow>
				<Switch bind:checked={s.enabled} label="Enabled" info="Whether the daemon starts the server" />
				{#if s.kind === ToolServerKind.FETCH}
					<Switch bind:checked={s.local} label="Local addresses" info="Lets fetches reach this host's own addresses, loopback and link local included" />
				{/if}
			</FieldRow>
		{/key}
	</FormSection>
	{#key generation}
		{#if s.kind === ToolServerKind.SEARCH}
			<FormSection title="Search" info="SearXNG answers only with its JSON format enabled">
				<FieldRow>
					<Segmented bind:value={s.provider} options={[{ value: 'searxng', label: 'SearXNG' }, { value: 'brave', label: 'Brave' }]} label="Provider" info="SearXNG or Brave Search" />
					{#if s.provider === 'brave'}
						<Secret bind:value={s.braveKey} label="Brave key" info="Brave Search API subscription token" set={s.secretsSet} />
					{:else}
						<Text bind:value={s.searxngUrl} label="SearXNG URL" info="Base URL of a SearXNG instance with the json format enabled" type="url" />
					{/if}
				</FieldRow>
			</FormSection>
		{:else if s.kind === ToolServerKind.STDIO}
			<FormSection title="Command" info="The process the daemon starts and speaks MCP with over its pipes">
				<FieldRow>
					<Text bind:value={s.command} label="Command" info="The executable that speaks MCP over stdio" mono required />
					<Chips bind:value={s.args} label="Arguments" info="Words after the command" />
				</FieldRow>
				<KeyValues bind:value={s.env} label="Environment" info="Variables the command runs with" secret secretsSet={s.secretsSet} />
			</FormSection>
		{:else if s.kind === ToolServerKind.HTTP}
			<FormSection title="Endpoint" info="The HTTP address the daemon speaks MCP with">
				<FieldRow>
					<Text bind:value={s.url} label="URL" info="The MCP endpoint" type="url" required />
				</FieldRow>
				<KeyValues bind:value={s.headers} label="Headers" info="Sent with every request to the endpoint" secret secretsSet={s.secretsSet} />
			</FormSection>
		{/if}
		<FormSection title="Tools" info="Which of the server's tools reach chats and routes">
			<FieldRow>
				<Chips bind:value={s.allowedTools} label="Allowed tools" info="Tools the server may offer, every tool when empty" />
			</FieldRow>
		</FormSection>
	{/key}
</Stack>
<SaveBar {dirty} always={!editing} {saving} {error} label={editing ? 'Save' : 'Create'} disabled={!s.name.trim() || (s.kind === ToolServerKind.STDIO && !s.command.trim()) || (s.kind === ToolServerKind.HTTP && !s.url.trim())} onsave={save} ondiscard={discard} />
