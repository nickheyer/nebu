<script lang="ts">
	import { clone, equals } from '@bufbuild/protobuf';
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoSortableItem, NeoSortableProvider } from '#lib/neo/sortable/index.js';
	import type { NeoSortableItem as SortItem } from '#lib/neo/sortable/neo-sortable-context.svelte.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { enumLabel, ms, num, when } from '#lib/format.js';
	import { ActionKind, BotSpecSchema, BotState, BotStateSchema, type Automation, type Bot, type BotActivity, type BotGuild, type BotSpec, type Persona } from '#lib/proto/nebu/v1/bot_pb.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import type { ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
	import { notify, toast } from '#lib/shell/notify.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import NameConfirm from '#lib/ui/NameConfirm.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Time from '#lib/ui/Time.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Textarea from '#lib/ui/fields/Textarea.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { botLamp, levelLamp } from '#lib/ui/status.js';
	import AutomationEditor from '#lib/pages/bots/AutomationEditor.svelte';
	import BehaviourTab from '#lib/pages/bots/BehaviourTab.svelte';
	import PersonaEditor from '#lib/pages/bots/PersonaEditor.svelte';
	import { actionWord, activityKinds, activityLevels, botTabs, duplicatePersona, isBotTab, newAutomation, newPersona, sendKinds, specCopy, triggerWord, withParts, type BotTab } from '#lib/pages/bots/bots.js';
	import ArrowLeftIcon from 'phosphor-svelte/lib/ArrowLeftIcon';
	import ArrowSquareOutIcon from 'phosphor-svelte/lib/ArrowSquareOutIcon';
	import DotsSixVerticalIcon from 'phosphor-svelte/lib/DotsSixVerticalIcon';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import RobotIcon from 'phosphor-svelte/lib/RobotIcon';

	let { bot, tab = 'status' }: { bot: Bot; tab?: string } = $props();

	const active = $derived<BotTab>(isBotTab(tab) ? tab : 'status');
	const running = $derived(bot.state !== BotState.STOPPED && bot.state !== BotState.UNSPECIFIED);
	const st = $derived(bot.status);

	// The settings tabs edit a copy of the spec, saved as a whole
	let spec = $state<BotSpec>(untrack(() => withParts(specCopy(bot))));
	let baseline = $state.raw<BotSpec>(untrack(() => withParts(specCopy(bot))));
	// Bumped whenever the copy is replaced so editors holding their own state start over
	let revision = $state(0);
	let saving = $state(false);
	let saveError = $state<string | undefined>(undefined);
	const dirty = $derived(!equals(BotSpecSchema, spec, baseline));

	function reset() {
		spec = withParts(specCopy(bot));
		baseline = withParts(specCopy(bot));
		revision++;
	}

	// A fresh copy whenever the saved spec changes while the form is clean, status updates ignored
	let seen = untrack(() => bot.spec);
	$effect(() => {
		const next = bot.spec;
		if (next === seen || (next && seen && equals(BotSpecSchema, next, seen))) return;
		seen = next;
		if (untrack(() => !dirty)) untrack(reset);
	});

	// Guilds and channels of a connected bot, listed again when its guild count moves
	const connected = $derived(bot.state === BotState.READY || bot.state === BotState.DEGRADED);
	const guildCount = $derived(st?.guilds ?? 0);
	let guilds = $state<BotGuild[]>([]);
	$effect(() => {
		void guildCount;
		if (!connected) {
			guilds = [];
			return;
		}
		rpc.bots
			.listBotGuilds({ botId: bot.id })
			.then((r) => (guilds = r.guilds))
			.catch((err) => toast.error('Guilds failed to load', reason(err)));
	});

	let servers = $state<ToolServerStatus[]>([]);
	$effect(() => {
		if (active !== 'personas') return;
		rpc.tools
			.listToolServers({ mesh: true })
			.then((r) => (servers = r.servers))
			.catch((err) => toast.error('Tool servers failed to load', reason(err)));
	});

	async function save() {
		saving = true;
		saveError = undefined;
		try {
			await rpc.bots.updateBot({ id: bot.id, enabled: bot.enabled, spec });
			baseline = clone(BotSpecSchema, spec);
			toast.success(`Saved ${bot.name}`, running ? 'The bot restarts' : undefined);
		} catch (err) {
			saveError = reason(err);
		} finally {
			saving = false;
		}
	}

	async function startStop() {
		try {
			if (running) await rpc.bots.stopBot({ id: bot.id });
			else await rpc.bots.startBot({ id: bot.id });
		} catch (err) {
			toast.error(running ? `Stop ${bot.name} failed` : `Start ${bot.name} failed`, reason(err));
		}
	}

	// Danger
	let newToken = $state('');
	let tokenBusy = $state(false);
	let deleteOpen = $state(false);
	async function changeToken() {
		tokenBusy = true;
		try {
			await rpc.bots.updateBot({ id: bot.id, enabled: bot.enabled, token: newToken, spec: bot.spec });
			toast.success('Token changed', running ? 'The bot restarts' : undefined);
			newToken = '';
		} catch (err) {
			toast.error('Token change failed', reason(err));
		} finally {
			tokenBusy = false;
		}
	}
	async function remove() {
		try {
			await rpc.bots.deleteBot({ id: bot.id });
			toast.success(`Deleted ${bot.name}`);
			void goto('/bots');
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}

	// Personas, the first one the default
	let personaIndex = $state<number | undefined>(undefined);
	let personaItems = $state<SortItem<{ id: string }>[]>([]);
	$effect(() => {
		personaItems = spec.personas.map((p) => ({ id: p.id, data: { id: p.id } }));
	});
	function reorderPersonas() {
		const order = personaItems.map((i) => String(i.id));
		const open = personaIndex === undefined ? undefined : spec.personas[personaIndex]?.id;
		spec.personas = [...spec.personas].sort((a, b) => order.indexOf(a.id) - order.indexOf(b.id));
		if (open !== undefined) personaIndex = spec.personas.findIndex((p) => p.id === open);
	}
	function addPersona() {
		spec.personas = [...spec.personas, newPersona()];
		personaIndex = spec.personas.length - 1;
	}
	function removePersona(i: number) {
		spec.personas = spec.personas.filter((_, j) => j !== i);
		personaIndex = undefined;
	}

	// Automations
	let automationIndex = $state<number | undefined>(undefined);
	function addAutomation() {
		spec.automations = [...spec.automations, newAutomation()];
		automationIndex = spec.automations.length - 1;
	}
	function removeAutomation(i: number) {
		spec.automations = spec.automations.filter((_, j) => j !== i);
		automationIndex = undefined;
	}

	// Activity, filtered by the daemon
	let level = $state<string | undefined>(undefined);
	let kind = $state<string | undefined>(undefined);
	const activity = new ListQuery<BotActivity, { level?: string; kind?: string }>(
		async (p, cursor, limit, signal) => {
			const res = await rpc.bots.listBotActivity({ botId: bot.id, level: p.level ?? '', kind: p.kind ?? '', cursor, limit }, { signal });
			return { items: res.activity, next: res.nextCursor };
		},
		[EventKind.BOT_ACTIVITY]
	);
	$effect(() => {
		if (active === 'activity') void activity.load({ level, kind });
	});
	function guildName(id: string): string {
		return guilds.find((g) => g.id === id)?.name ?? id;
	}
	function channelName(guild: string, id: string): string {
		const c = guilds.find((g) => g.id === guild)?.channels.find((ch) => ch.id === id);
		return c ? `#${c.name}` : id;
	}

	// Send
	let sendGuild = $state<string | undefined>(undefined);
	let sendChannel = $state<string | undefined>(undefined);
	let sendPersona = $state<string | undefined>(undefined);
	let sendKind = $state<ActionKind>(ActionKind.TEXT);
	let sendContent = $state('');
	let sending = $state(false);
	const sendChannels = $derived(guilds.find((g) => g.id === sendGuild)?.channels.map((c) => ({ value: c.id, label: `#${c.name}`, description: c.kind })) ?? []);
	async function send() {
		if (!sendChannel) return;
		sending = true;
		try {
			const res = await rpc.bots.sendBotMessage({ botId: bot.id, channelId: sendChannel, personaId: sendPersona ?? '', kind: sendKind, content: sendContent });
			const trace = res.trace;
			notify({
				type: 'success',
				title: 'Sent',
				content: `message ${res.messageId}`,
				duration: 5_000,
				close: true,
				actionProps: trace
					? {
							label: 'Trace',
							text: true,
							onclick: () => {
								void goto(`/traffic/${trace}`);
								return 'dismissed';
							}
						}
					: undefined
			});
			sendContent = '';
		} catch (err) {
			toast.error('Send failed', reason(err));
		} finally {
			sending = false;
		}
	}

	const tabs = botTabs.map((t) => ({ id: t.id, label: t.label }));
	const editorOpen = $derived((active === 'personas' && personaIndex !== undefined) || (active === 'automations' && automationIndex !== undefined));
	function closeEditor() {
		personaIndex = undefined;
		automationIndex = undefined;
	}
</script>

{#snippet avatar(url: string | undefined, size: number)}
	{#if url}
		<img class="avatar" src={url} alt="" width={size} height={size} />
	{:else}
		<span class="avatar blank" style:width="{size}px" style:height="{size}px"><RobotIcon size={16} /></span>
	{/if}
{/snippet}

{#snippet saveBar()}
	<SaveBar {dirty} {saving} error={saveError} message={running ? 'Saving restarts the bot' : undefined} onsave={save} ondiscard={reset} />
{/snippet}

{#snippet statusTab()}
	<Stack gap={24}>
		<KeyRows
			rows={[
				{ label: 'username', value: st?.username ?? '' },
				{ label: 'invite', value: st?.inviteUrl ?? '', mono: true, copy: true, tip: st?.inviteUrl },
				{ label: 'started', value: when(st?.startedAt) },
				{ label: 'guilds', value: st ? num(st.guilds) : '' },
				{ label: 'replies', value: st ? num(st.replies) : '' },
				{ label: 'images', value: st ? num(st.images) : '' },
				{ label: 'videos', value: st ? num(st.videos) : '' },
				{ label: 'errors', value: st ? num(st.errors) : '', color: st?.errors ? 'error' : undefined },
				{ label: 'error', value: bot.error, color: 'error', tip: bot.error }
			]}
		/>
		{#if st?.shards.length}
			<Section title="Shards" count={st.shards.length}>
				<div class="rows">
					{#each st.shards as s (s.id)}
						<ListRow
							lamp={s.connected ? 'success' : 'error'}
							shape={s.connected ? 'filled' : 'hollow'}
							primary="shard {s.id}"
							primaryWidth="10ch"
							primaryTip={[`connected ${s.connectedAt ? when(s.connectedAt) : 'never'}`, s.error].filter(Boolean).join('\n')}
							facts={[{ value: `${num(s.guilds)} guilds` }, { value: ms(s.latencyMs), tip: 'latency' }]}
						/>
					{/each}
				</div>
			</Section>
		{/if}
		<Section title="Danger">
			<FieldRow>
				<Secret bind:value={newToken} label="New token" set={bot.tokenSet} />
				<div class="act"><NeoButton elevation={2} loading={tokenBusy} disabled={!newToken} onclick={changeToken}>Change token</NeoButton></div>
			</FieldRow>
			<Row gap={12}>
				<NeoButton elevation={2} color="error" onclick={() => (deleteOpen = true)}>Delete bot</NeoButton>
			</Row>
		</Section>
	</Stack>
{/snippet}

{#snippet personasList()}
	<Stack gap={8}>
		<Row gap={12}>
			<Cell min="8rem" grow={false} push>
				<NeoButton elevation={2} onclick={addPersona}>{#snippet icon()}<PlusIcon size={20} />{/snippet}Persona</NeoButton>
			</Cell>
		</Row>
		{#if spec.personas.length === 0}
			<Empty text="No personas" action="Persona" onclick={addPersona} />
		{:else}
			<NeoSortableProvider bind:items={personaItems} axis="y" onDragEnd={reorderPersonas}>
				{#each personaItems as item, index (item.id)}
					{@const i = spec.personas.findIndex((p) => p.id === item.id)}
					{@const p = spec.personas[i]}
					{#if p}
						<NeoSortableItem id={item.id} {index} data={item.data}>
							{#snippet children({ instance })}
								<div {@attach instance.attach} class="sortrow">
									<ListRow
										primary={p.name}
										secondary={p.model || 'sole ready route'}
										secondaryWidth="24ch"
										selected={personaIndex === i}
										onclick={() => (personaIndex = i)}
										menu={[
											{ label: 'Edit', onclick: () => (personaIndex = i) },
											{ label: 'Duplicate', onclick: () => (spec.personas = [...spec.personas, duplicatePersona(p)]) },
											{ label: 'Delete', danger: true, confirm: `Delete ${p.name}?`, onclick: () => removePersona(i) }
										]}
									>
										{#snippet leading()}{@render avatar(p.avatarUrl, 24)}{/snippet}
										{#snippet badges()}{#if i === 0}<StatePill label="default" lamp="primary" />{/if}{/snippet}
										{#snippet action()}<span class="grip" aria-label="Drag" {@attach instance.attachHandle}><DotsSixVerticalIcon size={20} /></span>{/snippet}
									</ListRow>
								</div>
							{/snippet}
						</NeoSortableItem>
					{/if}
				{/each}
			</NeoSortableProvider>
		{/if}
	</Stack>
{/snippet}

{#snippet automationsList()}
	<Stack gap={8}>
		<Row gap={12}>
			<Cell min="8rem" grow={false} push>
				<NeoButton elevation={2} onclick={addAutomation}>{#snippet icon()}<PlusIcon size={20} />{/snippet}Automation</NeoButton>
			</Cell>
		</Row>
		{#if spec.automations.length === 0}
			<Empty text="No automations" action="Automation" onclick={addAutomation} />
		{:else}
			<div class="rows">
				{#each spec.automations as a, i (a.id)}
					<ListRow
						primary={a.name}
						secondary="{triggerWord(a.trigger?.kind ?? 0)} · {actionWord(a.action?.kind ?? 0)}"
						secondaryWidth="24ch"
						selected={automationIndex === i}
						onclick={() => (automationIndex = i)}
						menu={[
							{ label: 'Edit', onclick: () => (automationIndex = i) },
							{ label: 'Delete', danger: true, confirm: `Delete ${a.name}?`, onclick: () => removeAutomation(i) }
						]}
					>
						{#snippet leading()}<Switch bind:checked={a.enabled} />{/snippet}
					</ListRow>
				{/each}
			</div>
		{/if}
	</Stack>
{/snippet}

{#snippet editor()}
	{#key revision}
		{#if active === 'personas' && personaIndex !== undefined && spec.personas[personaIndex]}
			{@const i = personaIndex}
			{#key spec.personas[i]?.id}
				<PanelHeader title={spec.personas[i]?.name ?? ''} onclose={closeEditor} />
				<PersonaEditor bind:persona={() => spec.personas[i] as Persona, (v) => (spec.personas[i] = v)} {guilds} {servers} />
			{/key}
		{:else if active === 'automations' && automationIndex !== undefined && spec.automations[automationIndex]}
			{@const i = automationIndex}
			{#key spec.automations[i]?.id}
				<PanelHeader title={spec.automations[i]?.name ?? ''} onclose={closeEditor} />
				<AutomationEditor bind:automation={() => spec.automations[i] as Automation, (v) => (spec.automations[i] = v)} personas={spec.personas} {guilds} />
			{/key}
		{/if}
	{/key}
	{#if !viewport.ultrawide}{@render saveBar()}{/if}
{/snippet}

{#snippet listSide()}
	{#if active === 'personas'}{@render personasList()}{:else}{@render automationsList()}{/if}
{/snippet}

{#snippet activityTab()}
	<Stack gap={12}>
		<Row gap={12}>
			<Cell min="8rem" max="11rem"><Select bind:value={level} options={activityLevels} placeholder="Level" clearable /></Cell>
			<Cell min="9rem" max="12rem"><Select bind:value={kind} options={activityKinds} placeholder="Kind" clearable /></Cell>
		</Row>
		{#if !activity.settled}
			<Skeleton rows={5} />
		{:else if activity.error}
			<Empty text={activity.error} action="Retry" onclick={() => activity.refresh()} />
		{:else if activity.items.length === 0}
			<Empty text="No activity" />
		{:else}
			<div class="rows">
				{#each activity.items as a, i (i)}
					<ListRow
						lamp={levelLamp(a.level)}
						primary={a.kind}
						primaryWidth="12ch"
						primaryTip={[`persona ${a.persona || 'none'}`, `guild ${a.guildId ? guildName(a.guildId) : 'none'}`, `channel ${a.channelId ? channelName(a.guildId, a.channelId) : 'none'}`].join('\n')}
						secondary={a.message}
						secondaryKind="message"
						secondaryWidth="64ch"
					>
						{#snippet leading()}<span class="time"><Time value={a.at} /></span>{/snippet}
						{#snippet action()}
							{#if a.trace}
								<NeoButton text aria-label="Trace" href="/traffic/{a.trace}">{#snippet icon()}<ArrowSquareOutIcon size={20} />{/snippet}</NeoButton>
							{/if}
						{/snippet}
					</ListRow>
				{/each}
			</div>
			<LoadMore next={activity.next} loading={activity.loading} onmore={() => activity.more()} />
		{/if}
	</Stack>
{/snippet}

{#snippet sendTab()}
	<Stack gap={12}>
		<FieldRow>
			<Select bind:value={sendGuild} options={guilds.map((g) => ({ value: g.id, label: g.name }))} label="Guild" placeholder={connected ? 'Guild' : 'Not connected'} disabled={!connected} />
			<Select bind:value={sendChannel} options={sendChannels} label="Channel" disabled={!sendGuild} />
		</FieldRow>
		<FieldRow>
			<Select bind:value={sendPersona} options={spec.personas.map((p) => ({ value: p.id, label: p.name }))} label="Persona" clearable placeholder="The first persona" />
			<Segmented bind:value={sendKind} options={sendKinds} label="Kind" />
		</FieldRow>
		<Textarea bind:value={sendContent} label={sendKind === ActionKind.TEXT ? 'Message' : 'Prompt'} rows={3} />
		<Row gap={12} justify="flex-end">
			<NeoButton class="primary" elevation={2} loading={sending} disabled={!sendChannel || !sendContent} onclick={send}>Send</NeoButton>
		</Row>
	</Stack>
{/snippet}

<Stack gap={16}>
	<Row gap={12}>
		<Cell min="5rem" grow={false}>
			<NeoButton text href="/bots">{#snippet icon()}<ArrowLeftIcon size={20} />{/snippet}Bots</NeoButton>
		</Cell>
		<Cell min="12rem">
			<div class="title">
				{@render avatar(st?.avatarUrl, 32)}
				<h1 class="ellipsis" title={bot.name}>{bot.name}</h1>
				<StatePill label={enumLabel(BotStateSchema, bot.state)} lamp={botLamp(bot.state)} />
			</div>
		</Cell>
		<Cell min="5rem" grow={false} push>
			<Confirm label={running ? 'Stop' : 'Start'} question={running ? `Stop ${bot.name}?` : `Start ${bot.name}?`} danger={running} small={false} onconfirm={startStop} />
		</Cell>
	</Row>
	<PageTabs {tabs} {active} base="/bots/{bot.id}" />
	{#if active === 'status'}
		{@render statusTab()}
	{:else if active === 'personas' || active === 'automations'}
		<Sheet open={editorOpen} onclose={closeEditor} list={listSide} empty="Select a row to edit">{@render editor()}</Sheet>
	{:else if active === 'behaviour'}
		{#key revision}
			<BehaviourTab bind:spec {guilds} recommendedShards={st?.recommendedShards ?? 0} />
		{/key}
	{:else if active === 'activity'}
		{@render activityTab()}
	{:else}
		{@render sendTab()}
	{/if}
	{#if active === 'personas' || active === 'automations' || active === 'behaviour'}
		{@render saveBar()}
	{/if}
</Stack>

<NameConfirm bind:open={deleteOpen} title="Delete bot" name={bot.name} message="The bot disconnects first" onconfirm={remove} />

<style>
	.title {
		display: flex;
		gap: 12px;
		align-items: center;
		min-width: 0;
	}
	h1 {
		max-width: 24ch;
	}
	.avatar {
		display: inline-flex;
		flex: 0 0 auto;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
		object-fit: cover;
	}
	.avatar.blank {
		color: var(--nebu-text-muted);
		background: var(--neo-background-color-secondary);
	}
	.rows {
		display: flex;
		flex-direction: column;
	}
	.act.act {
		display: flex;
		flex: 0 0 auto;
		align-self: center;
	}
	.time {
		display: inline-block;
		min-width: 9ch;
		font-size: 13px;
	}
	.grip {
		display: inline-flex;
		color: var(--nebu-text-muted);
		cursor: grab;
	}
	.sortrow {
		position: relative;
	}
</style>
