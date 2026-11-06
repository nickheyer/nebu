<script lang="ts">
	import { clone, create, equals } from '@bufbuild/protobuf';
	import { untrack } from 'svelte';
	import { reason, rpc } from '#lib/api/transport.js';
	import { live } from '#lib/live/events.svelte.js';
	import { SettingsSchema, ToolServerKind, ToolServerSchema, type ToolServer } from '#lib/proto/nebu/v1/settings_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Chips from '#lib/ui/fields/Chips.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
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
	// Remounts the map and list fields when a discard resets them
	let generation = $state(0);

	const kinds = [
		{ value: ToolServerKind.SEARCH, label: 'search', description: 'Web search through SearXNG or Brave' },
		{ value: ToolServerKind.FETCH, label: 'fetch', description: 'Web pages read as text' },
		{ value: ToolServerKind.STDIO, label: 'stdio', description: 'A command speaking MCP' },
		{ value: ToolServerKind.HTTP, label: 'http', description: 'An MCP endpoint over HTTP' }
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

	// Writes the whole tool server list back with this one replaced in place or appended
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
<Stack gap={16}>
	<FieldRow>
		<Select bind:value={s.kind} options={kinds} label="Kind" disabled={editing} onchange={pickKind} />
		<Text bind:value={s.name} label="Name" max={64} info="Routes and personas attach the server by this name" />
	</FieldRow>
	{#key generation}
		{#if s.kind === ToolServerKind.SEARCH}
			<FieldRow>
				<Switch bind:checked={s.enabled} label="Enabled" />
				<Segmented bind:value={s.provider} options={[{ value: 'searxng', label: 'SearXNG' }, { value: 'brave', label: 'Brave' }]} label="Provider" />
			</FieldRow>
			{#if s.provider === 'brave'}
				<Secret bind:value={s.braveKey} label="Brave key" set={s.secretsSet} />
			{:else}
				<Text bind:value={s.searxngUrl} label="SearXNG URL" type="url" info="With the json format enabled" />
			{/if}
		{:else if s.kind === ToolServerKind.FETCH}
			<FieldRow>
				<Switch bind:checked={s.enabled} label="Enabled" />
				<Switch bind:checked={s.local} label="Local" info="Lets the server read this host's own addresses, which a model could use to reach the daemon's services" />
			</FieldRow>
		{:else if s.kind === ToolServerKind.STDIO}
			<FieldRow>
				<Switch bind:checked={s.enabled} label="Enabled" />
				<Text bind:value={s.command} label="Command" max={256} mono />
			</FieldRow>
			<FieldRow>
				<Chips bind:value={s.args} label="Arguments" />
				<KeyValues bind:value={s.env} label="Environment" secret secretsSet={s.secretsSet} />
			</FieldRow>
		{:else}
			<FieldRow>
				<Switch bind:checked={s.enabled} label="Enabled" />
				<Text bind:value={s.url} label="URL" type="url" />
			</FieldRow>
			<KeyValues bind:value={s.headers} label="Headers" secret secretsSet={s.secretsSet} />
		{/if}
		<Chips bind:value={s.allowedTools} label="Allowed tools" info="Every tool when empty" />
	{/key}
</Stack>
<SaveBar {dirty} {saving} {error} label={editing ? 'Save' : 'Create'} disabled={!s.name.trim() || (s.kind === ToolServerKind.STDIO && !s.command.trim()) || (s.kind === ToolServerKind.HTTP && !s.url.trim())} onsave={save} ondiscard={discard} />
