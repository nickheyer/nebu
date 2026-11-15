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
	import { toolKinds } from '#lib/pages/settings/webhooks.js';

	let { server, onclose }: { server?: ToolServer; onclose: () => void } = $props();

	const draft = untrack(() => server);
	const editing = draft !== undefined;
	let s = $state(draft ? clone(ToolServerSchema, draft) : create(ToolServerSchema, { enabled: true, kind: ToolServerKind.SEARCH, provider: 'searxng' }));
	const baseline = untrack(() => clone(ToolServerSchema, s));
	const dirty = $derived(!editing || !equals(ToolServerSchema, s, baseline));
	let saving = $state(false);
	let error = $state<string | undefined>(undefined);
	let generation = $state(0);

	const kinds = toolKinds;

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

	// What the server still needs, the first thing missing for its kind
	const missing = $derived.by(() => {
		if (!s.name.trim()) return 'Name the server';
		if (s.kind === ToolServerKind.SEARCH && s.provider === 'brave' && !s.braveKey.trim() && !draft?.braveKey) return 'Give the Brave key';
		if (s.kind === ToolServerKind.SEARCH && s.provider !== 'brave' && !s.searxngUrl.trim()) return 'Give the SearXNG URL';
		if (s.kind === ToolServerKind.STDIO && !s.command.trim()) return 'Give the command';
		if (s.kind === ToolServerKind.HTTP && !s.url.trim()) return 'Give the URL';
		return '';
	});
</script>

<PanelHeader title={draft ? draft.name : 'New tool server'} {onclose} />
<Stack gap={24}>
	<FormSection>
		<FieldRow>
			<Select bind:value={s.kind} options={kinds} label="Kind" disabled={editing} onchange={pickKind} />
			<Text bind:value={s.name} label="Name" required />
		</FieldRow>
		{#key generation}<Switch bind:checked={s.enabled} label="Enabled" info="A server turned off offers its tools to no chat and no bot" />{/key}
		{#if s.kind === ToolServerKind.FETCH}
			{#key generation}<Switch bind:checked={s.local} label="Local addresses" info="Lets fetches reach this host and its network" />{/key}
		{/if}
	</FormSection>
	{#key generation}
		{#if s.kind === ToolServerKind.SEARCH}
			<FormSection title="Search">
				<FieldRow>
					<Segmented bind:value={s.provider} options={[{ value: 'searxng', label: 'SearXNG' }, { value: 'brave', label: 'Brave' }]} label="Provider" />
					{#if s.provider === 'brave'}
						<Secret bind:value={s.braveKey} label="Brave key" required />
					{:else}
						<Text bind:value={s.searxngUrl} label="SearXNG URL" info="Needs its JSON format turned on" type="url" required />
					{/if}
				</FieldRow>
			</FormSection>
		{:else if s.kind === ToolServerKind.STDIO}
			<FormSection title="Command">
				<FieldRow>
					<Text bind:value={s.command} label="Command" mono required />
					<Chips bind:value={s.args} label="Arguments" />
				</FieldRow>
				<KeyValues bind:value={s.env} label="Environment" secret />
			</FormSection>
		{:else if s.kind === ToolServerKind.HTTP}
			<FormSection title="Endpoint">
				<FieldRow>
					<Text bind:value={s.url} label="URL" type="url" required />
				</FieldRow>
				<KeyValues bind:value={s.headers} label="Headers" secret />
			</FormSection>
		{/if}
		<FormSection title="Tools">
			<FieldRow>
				<Chips bind:value={s.allowedTools} label="Allowed tools" info="The tools of the server a chat or bot may call, by name" unset="every tool the server offers" />
			</FieldRow>
		</FormSection>
	{/key}
</Stack>
<SaveBar {dirty} always={!editing} {saving} {error} label={editing ? 'Save' : 'Create'} needs={missing} onsave={save} ondiscard={discard} />
