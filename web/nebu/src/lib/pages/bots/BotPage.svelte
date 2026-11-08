<script lang="ts">
	import { clone, equals } from '@bufbuild/protobuf';
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoSortableItem, NeoSortableProvider } from '#lib/neo/sortable/index.js';
	import type { NeoSortableItem as SortItem } from '#lib/neo/sortable/neo-sortable-context.svelte.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { ago, enumLabel, ms, num, when } from '#lib/format.js';
	import { ActionKind, BotSpecSchema, BotState, BotStateSchema, type Automation, type Bot, type BotActivity, type BotGuild, type BotSpec, type Persona } from '#lib/proto/nebu/v1/bot_pb.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import type { ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
	import { notify, toast } from '#lib/shell/notify.svelte.js';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import Facts, { type FactItem } from '#lib/ui/Facts.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import Menu from '#lib/ui/Menu.svelte';
	import NameConfirm from '#lib/ui/NameConfirm.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Textarea from '#lib/ui/fields/Textarea.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import Columns from '#lib/ui/layout/Columns.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { botLamp, levelLamp } from '#lib/ui/status.js';
	import AutomationEditor from '#lib/pages/bots/AutomationEditor.svelte';
	import Avatar from '#lib/pages/bots/Avatar.svelte';
	import BehaviourTab from '#lib/pages/bots/BehaviourTab.svelte';
	import PersonaEditor from '#lib/pages/bots/PersonaEditor.svelte';
	import { actionWord, activityKinds, activityLevels, botTabs, duplicatePersona, isBotTab, newAutomation, newPersona, sendKinds, specCopy, triggerWord, withParts, type BotTab } from '#lib/pages/bots/bots.js';
	import ArrowSquareOutIcon from 'phosphor-svelte/lib/ArrowSquareOutIcon';
	import DotsSixVerticalIcon from 'phosphor-svelte/lib/DotsSixVerticalIcon';
	import LightningIcon from 'phosphor-svelte/lib/LightningIcon';
	import MaskHappyIcon from 'phosphor-svelte/lib/MaskHappyIcon';
	import PlugsIcon from 'phosphor-svelte/lib/PlugsIcon';
	import PulseIcon from 'phosphor-svelte/lib/PulseIcon';

	let { bot, tab = 'status' }: { bot: Bot; tab?: string } = $props();

	const active = $derived<BotTab>(isBotTab(tab) ? tab : 'status');
	const running = $derived(bot.state !== BotState.STOPPED && bot.state !== BotState.UNSPECIFIED);
	const st = $derived(bot.status);
	const headerTabs = $derived(botTabs.map((t) => ({ id: t.id, label: t.label, href: `/bots/${bot.id}/${t.id}` })));

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

	async function start() {
		try {
			await rpc.bots.startBot({ id: bot.id });
		} catch (err) {
			toast.error(`Start ${bot.name} failed`, reason(err));
		}
	}

	async function stop() {
		try {
			await rpc.bots.stopBot({ id: bot.id });
		} catch (err) {
			toast.error(`Stop ${bot.name} failed`, reason(err));
		}
	}

	// Token and deletion
	let tokenOpen = $state(false);
	let newToken = $state('');
	let tokenBusy = $state(false);
	let deleteOpen = $state(false);

	function openToken() {
		newToken = '';
		tokenOpen = true;
	}

	async function changeToken() {
		tokenBusy = true;
		try {
			await rpc.bots.updateBot({ id: bot.id, enabled: bot.enabled, token: newToken.trim(), spec: bot.spec });
			toast.success('Token changed', running ? 'The bot restarts' : undefined);
			tokenOpen = false;
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

	const editorOpen = $derived((active === 'personas' && personaIndex !== undefined) || (active === 'automations' && automationIndex !== undefined));
	function closeEditor() {
		personaIndex = undefined;
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

	// Counters of the running connection with the shards' mean latency
	const facts = $derived.by((): FactItem[] => {
		const shards = st?.shards ?? [];
		const latency = shards.length ? shards.reduce((sum, s) => sum + s.latencyMs, 0) / shards.length : undefined;
		return [
			{ id: 'guilds', label: 'Guilds', value: num(st?.guilds) },
			{ id: 'shards', label: 'Shards', value: num(shards.length), tip: latency === undefined ? undefined : `mean latency ${ms(latency)}` },
			{ id: 'replies', label: 'Replies', value: num(st?.replies) },
			{ id: 'images', label: 'Images', value: num(st?.images) },
			{ id: 'videos', label: 'Videos', value: num(st?.videos) },
			{ id: 'errors', label: 'Errors', value: num(st?.errors), lamp: st?.errors ? 'error' : undefined }
		];
	});
</script>

{#snippet saveBar()}
	<SaveBar {dirty} {saving} error={saveError} message={running ? 'Saving restarts the bot' : undefined} onsave={save} ondiscard={reset} />
{/snippet}

{#snippet statusTab()}
	{#if st?.startedAt}
		<Card><Facts items={facts} /></Card>
	{/if}
	<Columns ratio="3/2">
		{#snippet left()}
			{#if guilds.length}
				<Card title="Guilds" count={guilds.length} flush>
					<List head={{ primary: 'Guild', facts: ['Members', 'Channels'] }}>
						{#each guilds as g (g.id)}
							<ListRow primary={g.name} primaryTip={g.id} facts={[{ value: num(g.members) }, { value: num(g.channels.length) }]} />
						{/each}
					</List>
				</Card>
			{/if}
			{#if st?.shards.length}
				<Card title="Shards" count={st.shards.length} flush>
					<List head={{ primary: 'Shard', facts: ['Guilds', 'Latency'] }}>
						{#each st.shards as s (s.id)}
							<ListRow
								lamp={s.connected ? 'success' : 'error'}
								shape={s.connected ? 'filled' : 'hollow'}
								primary="shard {s.id}"
								primaryTip={[`connected ${s.connectedAt ? when(s.connectedAt) : 'never'}`, s.error].filter(Boolean).join('\n')}
								facts={[{ value: num(s.guilds) }, { value: ms(s.latencyMs) }]}
							/>
						{/each}
					</List>
				</Card>
			{/if}
			{#if !guilds.length && !st?.shards.length}
				<Card>
					<Empty icon={PlugsIcon} text={running ? 'Connecting to Discord' : 'Not connected'} hint="Its guilds and shards show here once the bot is connected to Discord" />
				</Card>
			{/if}
		{/snippet}
		{#snippet right()}
			<Card title="Details">
				<KeyRows
					rows={[
						{ label: 'username', value: st?.username ?? '' },
						{ label: 'invite', value: st?.inviteUrl ?? '', href: st?.inviteUrl, copy: true, tip: st?.inviteUrl },
						{ label: 'started', value: st?.startedAt ? ago(st.startedAt, clockTick.now) : '', tip: when(st?.startedAt) || undefined },
						{ label: 'id', value: bot.id, mono: true, copy: true },
						{ label: 'created', value: when(bot.createdAt) },
						{ label: 'updated', value: when(bot.updatedAt) },
						{ label: 'enabled', value: bot.enabled ? 'yes' : 'no' },
						{ label: 'error', value: bot.error, color: 'error', tip: bot.error }
					]}
				/>
			</Card>
		{/snippet}
	</Columns>
{/snippet}

{#snippet personasList()}
	<Card title="Personas" count={spec.personas.length} info="The first persona answers unless an automation or wake word picks another" flush>
		{#if spec.personas.length === 0}
			<Empty icon={MaskHappyIcon} text="No personas" hint="Saved without one, the bot answers as a persona named after itself" action="New persona" onclick={addPersona} />
		{:else}
			<List head={{ primary: 'Persona', secondary: 'Model' }}>
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
												{ label: 'Duplicate', onclick: () => (spec.personas = [...spec.personas, duplicatePersona(p)]) },
												{ label: 'Delete', danger: true, divider: true, confirm: `Delete ${p.name}?`, onclick: () => removePersona(i) }
											]}
										>
											{#snippet leading()}
												<button type="button" class="grip" aria-label="Drag to reorder" {@attach instance.attachHandle}><DotsSixVerticalIcon size={18} /></button>
												<Avatar url={p.avatarUrl} size={24} />
											{/snippet}
											{#snippet badges()}{#if i === 0}<StatePill label="default" lamp="primary" />{/if}{/snippet}
										</ListRow>
									</div>
								{/snippet}
							</NeoSortableItem>
						{/if}
					{/each}
				</NeoSortableProvider>
			</List>
		{/if}
		{#if !editorOpen}<div class="commit">{@render saveBar()}</div>{/if}
	</Card>
{/snippet}

{#snippet automationsList()}
	<Card title="Automations" count={spec.automations.length} flush>
		{#if spec.automations.length === 0}
			<Empty icon={LightningIcon} text="No automations" hint="An automation acts on a schedule, a message, a keyword, a reaction or a command" action="New automation" onclick={addAutomation} />
		{:else}
			<List head={{ primary: 'Automation', secondary: 'Trigger', facts: ['Action'] }}>
				{#each spec.automations as a, i (a.id)}
					<ListRow
						primary={a.name}
						secondary={triggerWord(a.trigger?.kind ?? 0)}
						secondaryWidth="14ch"
						facts={[{ value: actionWord(a.action?.kind ?? 0) }]}
						selected={automationIndex === i}
						onclick={() => (automationIndex = i)}
						menu={[{ label: 'Delete', danger: true, confirm: `Delete ${a.name}?`, onclick: () => removeAutomation(i) }]}
					>
						{#snippet leading()}<Switch bind:checked={a.enabled} />{/snippet}
					</ListRow>
				{/each}
			</List>
		{/if}
		{#if !editorOpen}<div class="commit">{@render saveBar()}</div>{/if}
	</Card>
{/snippet}

{#snippet editor()}
	{#key revision}
		{#if active === 'personas' && personaIndex !== undefined && spec.personas[personaIndex]}
			{@const i = personaIndex}
			{#key spec.personas[i]?.id}
				<PanelHeader title={spec.personas[i]?.name ?? ''} subtitle={i === 0 ? 'default persona' : 'persona'} onclose={closeEditor} />
				<PersonaEditor bind:persona={() => spec.personas[i] as Persona, (v) => (spec.personas[i] = v)} {guilds} {servers} />
			{/key}
		{:else if active === 'automations' && automationIndex !== undefined && spec.automations[automationIndex]}
			{@const i = automationIndex}
			{#key spec.automations[i]?.id}
				<PanelHeader title={spec.automations[i]?.name ?? ''} subtitle="automation" onclose={closeEditor} />
				<AutomationEditor bind:automation={() => spec.automations[i] as Automation, (v) => (spec.automations[i] = v)} personas={spec.personas} {guilds} />
			{/key}
		{/if}
	{/key}
	{@render saveBar()}
{/snippet}

{#snippet activityTab()}
	<Card title="Activity" flush>
		{#snippet tools()}
			<span class="filter"><Select bind:value={level} options={activityLevels} placeholder="Level" clearable /></span>
			<span class="filter wide"><Select bind:value={kind} options={activityKinds} placeholder="Kind" clearable /></span>
		{/snippet}
		{#if !activity.settled}
			<Skeleton rows={5} primary="12ch" secondary="64ch" facts={1} menu={false} />
		{:else if activity.error}
			<Empty text={activity.error} action="Retry" onclick={() => activity.refresh()} />
		{:else if activity.items.length === 0}
			<Empty icon={PulseIcon} text={level || kind ? 'No activity matches' : 'No activity yet'} hint="Replies, images, commands, automations and shard events land here as the bot runs" />
		{:else}
			<List head={{ primary: 'Kind', secondary: 'Message', facts: ['When'] }}>
				{#each activity.items as a, i (i)}
					<ListRow
						lamp={levelLamp(a.level)}
						primary={a.kind}
						primaryWidth="12ch"
						primaryTip={[`persona ${a.persona || 'none'}`, `guild ${a.guildId ? guildName(a.guildId) : 'none'}`, `channel ${a.channelId ? channelName(a.guildId, a.channelId) : 'none'}`].join('\n')}
						secondary={a.message}
						secondaryKind="message"
						secondaryWidth="64ch"
						facts={[{ value: ago(a.at, clockTick.now), tip: when(a.at) }]}
					>
						{#snippet action()}
							{#if a.trace}
								<NeoButton text aria-label="Trace" title="Trace" href="/traffic/{a.trace}">{#snippet icon()}<ArrowSquareOutIcon size={18} />{/snippet}</NeoButton>
							{/if}
						{/snippet}
					</ListRow>
				{/each}
			</List>
			<LoadMore next={activity.next} loading={activity.loading} onmore={() => activity.more()} />
		{/if}
	</Card>
{/snippet}

{#snippet sendTab()}
	<Card title="New message">
		<FieldRow>
			<Select bind:value={sendGuild} options={guilds.map((g) => ({ value: g.id, label: g.name }))} label="Guild" placeholder={connected ? 'Guild' : 'Not connected'} disabled={!connected} />
			<Select bind:value={sendChannel} options={sendChannels} label="Channel" placeholder="Channel" disabled={!sendGuild} />
		</FieldRow>
		<FieldRow>
			<Select bind:value={sendPersona} options={spec.personas.map((p) => ({ value: p.id, label: p.name }))} label="Persona" clearable placeholder="The first persona" />
			<Segmented bind:value={sendKind} options={sendKinds} label="Kind" />
		</FieldRow>
		<Textarea bind:value={sendContent} label={sendKind === ActionKind.TEXT ? 'Message' : 'Prompt'} rows={3} />
		{#snippet footer()}
			{#if !connected}<span class="secondary">Start the bot to send</span>{/if}
			<span class="fill"></span>
			<NeoButton class="primary" elevation={2} loading={sending} disabled={!sendChannel || !sendContent.trim()} onclick={send}>Send</NeoButton>
		{/snippet}
	</Card>
{/snippet}

<PageHeader title={bot.name} back={{ label: 'Bots', href: '/bots' }} tabs={headerTabs} {active}>
	{#snippet meta()}
		{#if st?.avatarUrl}<Avatar url={st.avatarUrl} size={20} />{/if}
		<StatePill label={enumLabel(BotStateSchema, bot.state)} lamp={botLamp(bot.state)} />
		{#if st?.username}<span>{st.username}</span>{/if}
	{/snippet}
	{#snippet actions()}
		{#if running}
			<Confirm label="Stop" question="Stop {bot.name}?" danger small={false} onconfirm={stop} />
		{:else}
			<NeoButton elevation={2} onclick={start}>Start</NeoButton>
		{/if}
		<Menu
			items={[
				{ label: 'Change token', onclick: openToken },
				{ label: 'Delete bot', danger: true, divider: true, onclick: () => (deleteOpen = true) }
			]}
		/>
		{#if active === 'personas'}
			<NeoButton class="primary" elevation={2} onclick={addPersona}>New persona</NeoButton>
		{:else if active === 'automations'}
			<NeoButton class="primary" elevation={2} onclick={addAutomation}>New automation</NeoButton>
		{/if}
	{/snippet}
</PageHeader>

{#if active === 'status'}
	{@render statusTab()}
{:else if active === 'personas' || active === 'automations'}
	<Sheet open={editorOpen} onclose={closeEditor} list={active === 'personas' ? personasList : automationsList} width={640}>{@render editor()}</Sheet>
{:else if active === 'behaviour'}
	<Card>
		{#key revision}
			<BehaviourTab bind:spec {guilds} recommendedShards={st?.recommendedShards ?? 0} />
		{/key}
		{@render saveBar()}
	</Card>
{:else if active === 'activity'}
	{@render activityTab()}
{:else}
	{@render sendTab()}
{/if}

<Dialog bind:open={tokenOpen} title="Change token" confirm="Change" busy={tokenBusy} disabled={!newToken.trim()} onconfirm={changeToken}>
	<Secret bind:value={newToken} label="New token" set={bot.tokenSet} required message={running ? 'The bot restarts with the new token' : undefined} />
</Dialog>

<NameConfirm bind:open={deleteOpen} title="Delete bot" name={bot.name} message="The bot disconnects first" onconfirm={remove} />

<style>
	/* Keeps the save bar's buttons in from the edges of a flush card */
	.commit {
		padding: 0 var(--nebu-card-pad);
	}
	/* A sortable wrapper passes the list's columns through to its row and keeps the hairline between rows */
	.sortrow {
		display: grid;
		grid-template-columns: subgrid;
		grid-column: 1 / -1;
		position: relative;
	}
	.sortrow:not(:last-child) > :global(.rowwrap) {
		border-bottom-color: var(--nebu-border);
	}
	.grip {
		display: inline-flex;
		padding: 0;
		color: var(--nebu-text-muted);
		cursor: grab;
		background: none;
		border: 0;
	}
	.filter {
		display: inline-flex;
		width: 9rem;
	}
	.filter.wide {
		width: 11rem;
	}
</style>
