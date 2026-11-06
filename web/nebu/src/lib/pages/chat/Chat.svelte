<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { onMount, tick, untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoPill } from '#lib/neo/pill/index.js';
	import { NeoProgressBar } from '#lib/neo/progress/index.js';
	import { NeoSkeletonText } from '#lib/neo/skeletons/index.js';
	import { listModels, type ModelEntry } from '#lib/api/gateway.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { session as account } from '#lib/auth/session.svelte.js';
	import { pct } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { ChatTurnSchema, type Conversation } from '#lib/proto/nebu/v1/chat_pb.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import type { ToolServerStatus } from '#lib/proto/nebu/v1/tools_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { prefs } from '#lib/state/prefs.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import SearchBox from '#lib/ui/SearchBox.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Str from '#lib/ui/Str.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Slider from '#lib/ui/fields/Slider.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Textarea from '#lib/ui/fields/Textarea.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { byDay, ChatSession, chooseImages, imageSizes, modelMode, modelOptions, takesImages, videoFps, type PendingCall } from '#lib/pages/chat/chat.svelte.js';
	import SettingsPanel from '#lib/pages/chat/SettingsPanel.svelte';
	import Turn from '#lib/pages/chat/Turn.svelte';
	import ChatsIcon from 'phosphor-svelte/lib/ChatsIcon';
	import GearIcon from 'phosphor-svelte/lib/GearIcon';
	import PaperclipIcon from 'phosphor-svelte/lib/PaperclipIcon';
	import PaperPlaneRightIcon from 'phosphor-svelte/lib/PaperPlaneRightIcon';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import StopIcon from 'phosphor-svelte/lib/StopIcon';

	let { id = '' }: { id?: string } = $props();

	const session = new ChatSession();
	let models = $state<ModelEntry[]>([]);
	let modelsLoaded = $state(false);
	let modelsError = $state('');
	let servers = $state<ToolServerStatus[]>([]);
	let serversError = $state('');
	let query = $state('');
	let text = $state('');
	let attachments = $state<File[]>([]);
	let scroller = $state<HTMLDivElement>();
	let pinned = true;
	let listOpen = $state(false);
	let settingsOpen = $state(false);
	let openError = $state('');
	let renamingId = $state('');
	let renameTitle = $state('');
	let renameBusy = $state(false);

	const conversations = new ListQuery<Conversation, { query: string }>(async (p, cursor, limit, signal) => {
		const res = await rpc.chat.listConversations({ query: p.query, cursor, limit }, { signal });
		return { items: res.conversations, next: res.nextCursor };
	}, []);

	$effect(() => {
		void conversations.load({ query });
	});

	onMount(() => {
		void loadModels();
		void loadServers();
		let routeTimer: ReturnType<typeof setTimeout> | undefined;
		let toolTimer: ReturnType<typeof setTimeout> | undefined;
		const stops = [
			live.on(EventKind.ROUTE, () => {
				clearTimeout(routeTimer);
				routeTimer = setTimeout(() => void loadModels(), 300);
			}),
			live.on(EventKind.TOOL_SERVER, () => {
				clearTimeout(toolTimer);
				toolTimer = setTimeout(() => void loadServers(), 300);
			}),
			live.on(EventKind.TRACE, (ev) => {
				if (ev.payload.case === 'trace') session.observe(ev.payload.value);
			})
		];
		return () => {
			clearTimeout(routeTimer);
			clearTimeout(toolTimer);
			stops.forEach((stop) => stop());
		};
	});

	// Lists the gateway's routes, keeping the pick while it stays ready and else taking the first ready text route
	async function loadModels() {
		try {
			models = await listModels();
			modelsError = '';
			if (!models.some((m) => m.ready && m.id === session.model)) {
				const ready = models.filter((m) => m.ready);
				session.model = (ready.find((m) => modelMode(m) === 'text') ?? ready[0])?.id ?? '';
			}
		} catch (err) {
			modelsError = reason(err);
			toast.error('Models failed to list', modelsError);
		} finally {
			modelsLoaded = true;
		}
	}

	async function loadServers() {
		try {
			servers = (await rpc.tools.listToolServers({ mesh: true })).servers;
			serversError = '';
		} catch (err) {
			serversError = reason(err);
			toast.error('Tool servers failed to list', serversError);
		}
	}

	// Asks the gateway for its routes on every search, answering the ready ones holding the typed words
	async function fetchModels(q: string, _cursor: string, _limit: number, signal: AbortSignal) {
		models = await listModels(signal);
		return { items: modelOptions(models, q), next: '' };
	}

	// Opens the conversation the route names, a bare route starting a new one
	$effect(() => {
		const want = id;
		untrack(() => {
			openError = '';
			pinned = true;
			if (!want) {
				if (!session.isNew) session.newConversation();
				return;
			}
			if (session.conversation.id === want) return;
			session.open(want).catch((err) => {
				openError = reason(err);
				toast.error('Conversation failed to open', openError);
			});
		});
	});

	// Moves a new conversation to its own route once its first turn is saved
	$effect(() => {
		if (!id && session.saved) void goto(`/chat/${session.saved}`, { replace: true });
	});

	const current = $derived(models.find((m) => m.id === session.model));
	const mode = $derived(modelMode(current));
	const media = $derived(mode === 'image' || mode === 'video');
	const attach = $derived(takesImages(current));
	const anyReady = $derived(models.some((m) => m.ready));
	const canSend = $derived(text.trim() !== '' && session.model !== '' && !session.busy && !session.opening);
	const shown = $derived(session.turns.filter((t) => t.role !== 'tool'));
	const last = $derived(session.turns[session.turns.length - 1]);
	const lastAssistant = $derived(session.turns.findLast((t) => t.role === 'assistant'));
	const userName = $derived(session.settings.userName || 'You');
	const assistantName = $derived(session.settings.assistantName || session.conversation.model || session.model || 'Model');
	const draftTurn = $derived(create(ChatTurnSchema, { role: 'assistant', text: session.draft, toolCalls: session.draftCalls }));
	const groups = $derived(byDay(conversations.items));
	const sizeOptions = imageSizes.map((s) => ({ value: s, label: s }));
	const fpsOptions = videoFps.map((f) => ({ value: f, label: String(f) }));

	// Follows the newest turn while the person has not scrolled up
	$effect(() => {
		void session.draft;
		void session.draftCalls.length;
		void session.turns.length;
		void session.pending.length;
		void session.busy;
		if (!pinned) return;
		void tick().then(() => {
			if (scroller) scroller.scrollTop = scroller.scrollHeight;
		});
	});

	function onscroll() {
		if (scroller) pinned = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 64;
	}

	async function send() {
		if (!canSend) return;
		const prompt = text.trim();
		const files = attach ? attachments : [];
		text = '';
		attachments = [];
		pinned = true;
		try {
			if (mode === 'image') await session.makeImages(prompt);
			else if (mode === 'video') await session.makeVideo(prompt);
			else await session.send(prompt, files, servers);
		} catch (err) {
			text = prompt;
			attachments = files;
			toast.error('Send failed', reason(err));
		}
		conversations.refresh();
	}

	// Runs a step of the conversation, its failure raising a toast
	async function attempt(title: string, step: () => Promise<void>) {
		pinned = true;
		try {
			await step();
		} catch (err) {
			toast.error(title, reason(err));
		}
		conversations.refresh();
	}

	// Enter sends, shift and enter starts a new line
	function onkeydown(e: KeyboardEvent) {
		if (e.key !== 'Enter' || e.shiftKey || e.isComposing) return;
		e.preventDefault();
		void send();
	}

	async function attachImages() {
		attachments = [...attachments, ...(await chooseImages())].slice(0, 8);
	}

	function toggleSettings() {
		if (viewport.ultrawide) prefs.chatSettingsOpen = !prefs.chatSettingsOpen;
		else settingsOpen = true;
	}

	function newConversation() {
		listOpen = false;
		void goto('/chat');
	}

	function openConversation(c: Conversation) {
		listOpen = false;
		void goto(`/chat/${c.id}`);
	}

	function startRename(c: Conversation) {
		renamingId = c.id;
		renameTitle = c.title;
	}

	async function rename() {
		const title = renameTitle.trim();
		if (!title) return;
		renameBusy = true;
		try {
			await rpc.chat.renameConversation({ id: renamingId, title });
			if (session.conversation.id === renamingId) session.conversation.title = title;
			renamingId = '';
			conversations.refresh();
		} catch (err) {
			toast.error('Rename failed', reason(err));
		} finally {
			renameBusy = false;
		}
	}

	async function remove(c: Conversation) {
		try {
			await rpc.chat.deleteConversation({ id: c.id });
			if (session.conversation.id === c.id) void goto('/chat');
			conversations.refresh();
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}
</script>

{#snippet conversationList()}
	<Stack gap={12}>
		<Row gap={8}>
			<Cell grow={false}>
				<NeoButton elevation={2} onclick={newConversation}>{#snippet icon()}<PlusIcon size={20} />{/snippet}New</NeoButton>
			</Cell>
		</Row>
		<SearchBox bind:value={query} placeholder="Search" width="100%" />
		{#if !conversations.settled}
			<Skeleton rows={6} />
		{:else if conversations.error}
			<Empty text={conversations.error} action="Retry" onclick={() => conversations.refresh()} />
		{:else if conversations.items.length === 0}
			{#if query}
				<Empty text="No matches" action="Clear search" onclick={() => (query = '')} />
			{:else}
				<Empty text="No conversations" />
			{/if}
		{:else}
			<Stack gap={8}>
				{#each groups as g (g.label)}
					<Stack gap={0}>
						<span class="label day">{g.label}</span>
						{#each g.items as c (c.id)}
							{#snippet renameBox()}
								<div class="rename">
									<Stack gap={8}>
										<Text bind:value={renameTitle} label="Title" max={64} />
										<Row gap={8} justify="flex-end">
											<NeoButton text onclick={() => (renamingId = '')}>Cancel</NeoButton>
											<NeoButton class="primary" elevation={2} loading={renameBusy} disabled={!renameTitle.trim()} onclick={rename}>Rename</NeoButton>
										</Row>
									</Stack>
								</div>
							{/snippet}
							<ListRow
								primary={c.title || 'Untitled'}
								selected={c.id === session.conversation.id}
								onclick={() => openConversation(c)}
								below={renamingId === c.id ? renameBox : undefined}
								menu={[
									{ label: 'Rename', onclick: () => startRename(c) },
									{ label: 'Delete', danger: true, confirm: `Delete ${c.title || 'Untitled'}`, onclick: () => remove(c) }
								]}
							/>
						{/each}
					</Stack>
				{/each}
			</Stack>
			<LoadMore next={conversations.next} loading={conversations.loading} onmore={() => conversations.more()} />
		{/if}
	</Stack>
{/snippet}

{#snippet settingsView(onclose: () => void)}
	<PanelHeader title="Settings" {onclose} />
	{#key session.conversation.id}
		<SettingsPanel bind:settings={() => session.settings, (v) => (session.conversation.settings = v)} {servers} {serversError} contextLength={current?.inventory.context_length ?? 0} />
	{/key}
{/snippet}

{#snippet sendButton()}
	{#if session.busy}
		<NeoButton elevation={2} onclick={() => session.stop()}>{#snippet icon()}<StopIcon size={20} />{/snippet}Stop</NeoButton>
	{:else}
		<NeoButton class="primary" elevation={2} disabled={!canSend} onclick={send}>{#snippet icon()}<PaperPlaneRightIcon size={20} />{/snippet}Send</NeoButton>
	{/if}
{/snippet}

{#snippet textbox(placeholder: string)}
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="box" {onkeydown}>
		<Textarea bind:value={text} {placeholder} rows={1} maxRows={8} />
	</div>
{/snippet}

{#snippet header()}
	<Row gap={8} wrap={false}>
		{#if viewport.mobile}
			<Cell grow={false}>
				<NeoButton text aria-label="Conversations" onclick={() => (listOpen = true)}>{#snippet icon()}<ChatsIcon size={20} />{/snippet}</NeoButton>
			</Cell>
		{/if}
		<Cell min="10rem" max="24rem">
			<Picker bind:value={() => session.model || undefined, (v) => (session.model = v ?? '')} fetch={fetchModels} placeholder="Model" current={session.model} />
		</Cell>
		<Cell grow={false} push>
			<NeoButton text aria-label="Settings" onclick={toggleSettings}>{#snippet icon()}<GearIcon size={20} />{/snippet}</NeoButton>
		</Cell>
	</Row>
{/snippet}

{#snippet messages()}
	{#if session.opening}
		<Stack gap={24}>
			{#each { length: 3 } as _, i (i)}
				<Stack gap={8}>
					<NeoSkeletonText lines={1} width="12ch" />
					<NeoSkeletonText lines={2} width="64ch" />
				</Stack>
			{/each}
		</Stack>
	{:else if openError && shown.length === 0}
		<Empty text={openError} action="New" onclick={newConversation} />
	{:else if shown.length === 0 && !session.busy}
		{#if !modelsLoaded}
			<Stack gap={8}>
				<NeoSkeletonText lines={1} width="12ch" />
				<NeoSkeletonText lines={2} width="64ch" />
			</Stack>
		{:else if modelsError}
			<Empty text={modelsError} action="Retry" onclick={loadModels} />
		{:else if !anyReady}
			<Empty text="No ready route" action={account.reads ? 'Serve' : undefined} href={account.reads ? '/serve' : undefined} />
		{:else}
			<Empty text="No messages" />
		{/if}
	{/if}
	{#each shown as turn, i (i)}
		<Turn
			{turn}
			{session}
			name={turn.role === 'user' ? userName : assistantName}
			traceLinks={account.reads}
			pending={turn === lastAssistant ? session.pending : []}
			onretry={turn === last && !session.busy && (turn.error || turn.stop === 'cancelled') ? () => attempt('Retry failed', () => session.retry(servers, mode)) : undefined}
			onrun={(c: PendingCall) => attempt('Tool call failed', () => session.runCall(c, servers))}
			onskip={(c: PendingCall) => attempt('Tool call failed', () => session.skipCall(c, servers))}
			onresult={(c: PendingCall, result: string) => attempt('Tool call failed', () => session.settle(c, result, false, servers))}
		/>
	{/each}
	{#if session.busy}
		{#if mode === 'video'}
			<div class="progress">
				<Stack gap={8}>
					<Str value={assistantName} weight={500} />
					<Row gap={12} wrap={false}>
						<Cell min="8rem" max="32rem">
							<NeoProgressBar value={session.videoProgress ?? 0} elevation={-1} height="8px" color="primary" />
						</Cell>
						<Cell grow={false}>
							<span class="num secondary">{session.videoProgress === null ? 'Queued' : pct(session.videoProgress)}</span>
						</Cell>
					</Row>
				</Stack>
			</div>
		{:else if mode === 'image'}
			<div class="progress">
				<Stack gap={8}>
					<Str value={assistantName} weight={500} />
					<Row gap={8}>
						{#each { length: session.media.imageCount } as _, i (i)}
							<NeoSkeletonText lines={1} width="256px" height="256px" />
						{/each}
					</Row>
				</Stack>
			</div>
		{:else}
			<Turn turn={draftTurn} {session} name={assistantName} streaming />
		{/if}
	{/if}
{/snippet}

{#snippet composer()}
	{#if media}
		<Stack gap={12}>
			{@render textbox('Prompt')}
			<Row gap={12} align="flex-end">
				<Cell min="16rem">
					<FieldRow>
						<Select value={session.media.imageSize} options={sizeOptions} label="Size" onchange={(v) => v && (session.media.imageSize = v)} />
						<Slider bind:value={session.media.imageSteps} min={1} max={100} label="Steps" />
						{#if mode === 'image'}
							<Slider bind:value={session.media.imageCount} min={1} max={4} label="Count" />
						{:else}
							<Slider bind:value={session.media.videoSeconds} min={1} max={30} unit="s" label="Seconds" />
							<Select value={session.media.videoFps} options={fpsOptions} label="FPS" onchange={(v) => v && (session.media.videoFps = v)} />
						{/if}
					</FieldRow>
				</Cell>
				<Cell grow={false} push>{@render sendButton()}</Cell>
			</Row>
		</Stack>
	{:else}
		<Stack gap={8}>
			{#if attach && attachments.length}
				<Row gap={4}>
					{#each attachments as f, i (i)}
						<NeoPill size="small" close label={f.name} onClose={() => (attachments = attachments.filter((_, j) => j !== i))} />
					{/each}
				</Row>
			{/if}
			<Row gap={8} align="flex-end" wrap={false}>
				{#if attach}
					<Cell grow={false}>
						<NeoButton text aria-label="Attach" onclick={attachImages}>{#snippet icon()}<PaperclipIcon size={20} />{/snippet}</NeoButton>
					</Cell>
				{/if}
				<Cell min="8rem">{@render textbox('Message')}</Cell>
				<Cell grow={false}>{@render sendButton()}</Cell>
			</Row>
		</Stack>
	{/if}
{/snippet}

<div class="chat" class:mobile={viewport.mobile}>
	{#if viewport.ultrawide}
		<aside class="conversations">{@render conversationList()}</aside>
	{:else if viewport.standard}
		<aside class="strip">
			<Stack gap={4}>
				<NeoButton text aria-label="New conversation" onclick={newConversation}>{#snippet icon()}<PlusIcon size={20} />{/snippet}</NeoButton>
				<NeoButton text aria-label="Conversations" onclick={() => (listOpen = true)}>{#snippet icon()}<ChatsIcon size={20} />{/snippet}</NeoButton>
			</Stack>
		</aside>
	{/if}
	<section class="main">
		{@render header()}
		<div class="scroll hairline" bind:this={scroller} {onscroll}>
			{@render messages()}
		</div>
		<div class="composer hairline">{@render composer()}</div>
	</section>
	{#if viewport.ultrawide && prefs.chatSettingsOpen}
		<aside class="settings">{@render settingsView(() => (prefs.chatSettingsOpen = false))}</aside>
	{/if}
</div>

{#if !viewport.ultrawide}
	<Sheet bind:open={listOpen}>
		<PanelHeader title="Conversations" onclose={() => (listOpen = false)} />
		{@render conversationList()}
	</Sheet>
	<Sheet bind:open={settingsOpen}>{@render settingsView(() => (settingsOpen = false))}</Sheet>
{/if}

<style>
	.chat {
		display: flex;
		gap: 24px;
		height: calc(100dvh - 96px);
		min-height: 0;
	}
	.chat.mobile {
		height: calc(100dvh - 136px);
	}
	.conversations {
		flex: 0 0 280px;
		min-width: 0;
		overflow-x: hidden;
		overflow-y: auto;
	}
	.settings {
		flex: 0 0 320px;
		min-width: 0;
		overflow-x: hidden;
		overflow-y: auto;
	}
	.strip {
		flex: 0 0 auto;
	}
	.main {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
		min-height: 0;
	}
	.scroll {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		min-height: 0;
		padding: 8px 8px 0;
		overflow-x: hidden;
		overflow-y: auto;
	}
	.composer {
		padding-top: 12px;
	}
	.box {
		flex: 1 1 auto;
		min-width: 0;
	}
	.day {
		padding: 8px 8px 4px;
	}
	.rename {
		flex: 1 1 auto;
		min-width: 0;
		padding-top: 4px;
	}
	.progress {
		padding: 12px 0;
	}
</style>
