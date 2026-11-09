<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { onMount, untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { NeoButton, NeoCloseButton } from '#lib/neo/buttons/index.js';
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
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
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
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import Toolbar from '#lib/ui/layout/Toolbar.svelte';
	import { byDay, ChatSession, chooseImages, imageSizes, modelMode, modelOptions, takesImages, videoFps, type PendingCall } from '#lib/pages/chat/chat.svelte.js';
	import SettingsPanel from '#lib/pages/chat/SettingsPanel.svelte';
	import Turn from '#lib/pages/chat/Turn.svelte';
	import ChatsCircleIcon from 'phosphor-svelte/lib/ChatsCircleIcon';
	import ChatsIcon from 'phosphor-svelte/lib/ChatsIcon';
	import GearIcon from 'phosphor-svelte/lib/GearIcon';
	import PaperclipIcon from 'phosphor-svelte/lib/PaperclipIcon';
	import PaperPlaneRightIcon from 'phosphor-svelte/lib/PaperPlaneRightIcon';
	import PlayIcon from 'phosphor-svelte/lib/PlayIcon';
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
	let flow = $state<HTMLDivElement>();
	let pinned = true;
	let listOpen = $state(false);
	let settingsOpen = $state(false);
	let openError = $state('');
	let renaming = $state<Conversation>();
	let renameOpen = $state(false);
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

	// Lists the gateway's routes, taking the first ready text route when the pick is gone
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

	async function fetchModels(q: string, _cursor: string, _limit: number, signal: AbortSignal) {
		models = await listModels(signal);
		return { items: modelOptions(models, q), next: '' };
	}

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
	const noRoute = $derived(modelsLoaded && !modelsError && !anyReady);
	const quiet = $derived(shown.length === 0 && !session.busy);
	const columns = $derived(viewport.ultrawide);
	const settingsColumn = $derived(columns && prefs.chatSettingsOpen);
	const sizeOptions = imageSizes.map((s) => ({ value: s, label: s }));
	const fpsOptions = videoFps.map((f) => ({ value: f, label: String(f) }));

	// Follows the thread as it grows until the person scrolls up
	$effect(() => {
		if (!flow) return;
		const watcher = new ResizeObserver(() => {
			if (pinned && scroller) scroller.scrollTop = scroller.scrollHeight;
		});
		watcher.observe(flow);
		return () => watcher.disconnect();
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
		if (columns) prefs.chatSettingsOpen = !prefs.chatSettingsOpen;
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
		renaming = c;
		renameTitle = c.title;
		renameOpen = true;
	}

	async function rename() {
		const title = renameTitle.trim();
		const target = renaming;
		if (!title || !target) return;
		renameBusy = true;
		try {
			await rpc.chat.renameConversation({ id: target.id, title });
			if (session.conversation.id === target.id) session.conversation.title = title;
			renameOpen = false;
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

{#snippet search()}
	<Toolbar>
		<span class="grow"><SearchBox bind:value={query} width="100%" /></span>
		<NeoButton elevation={2} aria-label="New conversation" onclick={newConversation}>{#snippet icon()}<PlusIcon size={20} />{/snippet}</NeoButton>
	</Toolbar>
{/snippet}

{#snippet conversationList()}
	{#if !conversations.settled}
		<Skeleton rows={6} dot={false} primary="20ch" secondary={false} facts={0} action={false} />
	{:else if conversations.error}
		<Empty text={conversations.error} action="Retry" onclick={() => conversations.refresh()} />
	{:else if conversations.items.length === 0}
		{#if query}
			<Empty text="No matches" action="Clear search" onclick={() => (query = '')} />
		{:else}
			<Empty text="No conversations" />
		{/if}
	{:else}
		{#each groups as g (g.label)}
			<List head={{ primary: g.label }} label={g.label}>
				{#each g.items as c (c.id)}
					<ListRow
						primary={c.title || 'Untitled'}
						selected={c.id === session.conversation.id}
						onclick={() => openConversation(c)}
						menu={[
							{ label: 'Rename', onclick: () => startRename(c) },
							{ label: 'Delete', danger: true, confirm: `Delete ${c.title || 'Untitled'}?`, onclick: () => remove(c) }
						]}
					/>
				{/each}
			</List>
		{/each}
		<LoadMore next={conversations.next} loading={conversations.loading} onmore={() => conversations.more()} />
	{/if}
{/snippet}

{#snippet settingsForm()}
	{#key session.conversation.id}
		<SettingsPanel bind:settings={() => session.settings, (v) => (session.conversation.settings = v)} {servers} {serversError} contextLength={current?.inventory.context_length ?? 0} assistantFallback={session.conversation.model || session.model} />
	{/key}
{/snippet}

{#snippet head()}
	<Toolbar>
		{#if !columns}
			<NeoButton text aria-label="Conversations" onclick={() => (listOpen = true)}>{#snippet icon()}<ChatsIcon size={20} />{/snippet}</NeoButton>
		{/if}
		<span class="model">
			<Picker bind:value={() => session.model || undefined, (v) => (session.model = v ?? '')} fetch={fetchModels} label="Model" compact disabled={noRoute} current={session.model} />
		</span>
		<span class="push">
			<NeoButton text aria-label="Settings" aria-pressed={columns ? settingsColumn : undefined} onclick={toggleSettings}>{#snippet icon()}<GearIcon size={20} />{/snippet}</NeoButton>
		</span>
	</Toolbar>
{/snippet}

{#snippet messages()}
	{#if session.opening}
		<Skeleton kind="text" rows={6} />
	{:else if openError && shown.length === 0}
		<Empty icon={ChatsCircleIcon} text="Conversation failed to open" hint={openError} action="New" onclick={newConversation} />
	{:else if quiet && !modelsLoaded}
		<Skeleton kind="text" rows={2} />
	{:else if quiet && modelsError}
		<Empty icon={PlayIcon} text="Routes failed to list" hint={modelsError} action="Retry" onclick={loadModels} />
	{:else if quiet && !anyReady}
		{#if account.reads}
			<Empty icon={PlayIcon} text="No ready route" action="Launch" href="/serve/launch" primary />
		{:else}
			<Empty icon={PlayIcon} text="No ready route" />
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
				<Str value={assistantName} weight={500} />
				<div class="track">
					<span class="bar"><NeoProgressBar value={session.videoProgress ?? 0} elevation={-1} height="8px" width="100%" color="primary" /></span>
					<span class="num secondary">{session.videoProgress === null ? 'Queued' : pct(session.videoProgress)}</span>
				</div>
			</div>
		{:else if mode === 'image'}
			<div class="progress">
				<Str value={assistantName} weight={500} />
				<div class="tiles">
					{#each { length: session.media.imageCount } as _, i (i)}
						<NeoSkeletonText lines={1} width="256px" height="256px" />
					{/each}
				</div>
			</div>
		{:else}
			<Turn turn={draftTurn} {session} name={assistantName} streaming />
		{/if}
	{/if}
{/snippet}

{#snippet sendButton()}
	{#if session.busy}
		<NeoButton elevation={2} onclick={() => session.stop()}>{#snippet icon()}<StopIcon size={20} />{/snippet}Stop</NeoButton>
	{:else}
		<NeoButton class="primary" elevation={2} disabled={!canSend} onclick={send}>{#snippet icon()}<PaperPlaneRightIcon size={20} />{/snippet}Send</NeoButton>
	{/if}
{/snippet}

{#snippet composer()}
	<div class="composer">
		{#if media}
			<FieldRow>
				<Select value={session.media.imageSize} options={sizeOptions} label="Size" info="Width x height" onchange={(v) => v && (session.media.imageSize = v)} />
				<Slider bind:value={session.media.imageSteps} min={1} max={100} label="Steps" info="Denoising steps" />
				{#if mode === 'image'}
					<Slider bind:value={session.media.imageCount} min={1} max={4} label="Count" info="Images per request" />
				{:else}
					<Slider bind:value={session.media.videoSeconds} min={1} max={30} unit="s" label="Seconds" info="Clip length" />
					<Select value={session.media.videoFps} options={fpsOptions} label="FPS" info="Frames per second" onchange={(v) => v && (session.media.videoFps = v)} />
				{/if}
			</FieldRow>
		{:else if attach && attachments.length}
			<div class="pills">
				{#each attachments as f, i (i)}
					<NeoPill size="small" close label={f.name} onClose={() => (attachments = attachments.filter((_, j) => j !== i))} />
				{/each}
			</div>
		{/if}
		<div class="line">
			{#if attach && !media}
				<span class="beside"><NeoButton text aria-label="Attach" onclick={attachImages}>{#snippet icon()}<PaperclipIcon size={20} />{/snippet}</NeoButton></span>
			{/if}
			<!-- svelte-ignore a11y_no_static_element_interactions -->
			<div class="box" {onkeydown}>
				<Textarea bind:value={text} placeholder={media ? 'Prompt' : 'Message'} rows={1} maxRows={8} disabled={session.model === ''} />
			</div>
			<span class="beside">{@render sendButton()}</span>
		</div>
	</div>
{/snippet}

<PageHeader title={id && session.conversation.title ? session.conversation.title : undefined} info="Talk to any ready route through the gateway, with tools, images and video where the model allows" />

<div class="chat" class:mobile={viewport.mobile}>
	{#if columns}
		<div class="col side">
			<Card flush grow tools={search}>
				<div class="scroll">{@render conversationList()}</div>
			</Card>
		</div>
	{/if}
	<div class="col main">
		<Card flush grow tools={head} footer={composer}>
			<div class="scroll thread hairline" class:centred={quiet && !session.opening && (!anyReady || !!modelsError || !!openError)} bind:this={scroller} {onscroll}>
				<div class="flow" bind:this={flow}>{@render messages()}</div>
			</div>
		</Card>
		{#if !columns}
			<Sheet bind:open={listOpen} width={360}>
				<PanelHeader title="Conversations" onclose={() => (listOpen = false)} />
				<div class="sheetsearch">{@render search()}</div>
				<div class="sheetlist">{@render conversationList()}</div>
			</Sheet>
			<Sheet bind:open={settingsOpen} width={400}>
				<PanelHeader title="Settings" onclose={() => (settingsOpen = false)} />
				{@render settingsForm()}
			</Sheet>
		{/if}
		<Dialog bind:open={renameOpen} title="Rename conversation" confirm="Rename" busy={renameBusy} disabled={!renameTitle.trim()} onconfirm={rename}>
			<Text bind:value={renameTitle} label="Title" info="The conversation's name in the list" max={64} />
		</Dialog>
	</div>
	{#if settingsColumn}
		<div class="col settings">
			<Card title="Settings" flush grow>
				{#snippet tools()}<NeoCloseButton text aria-label="Close settings" onclick={() => (prefs.chatSettingsOpen = false)} />{/snippet}
				<div class="scroll form">{@render settingsForm()}</div>
			</Card>
		</div>
	{/if}
</div>

<style>
	/* Fills the viewport under the top bar so only the columns scroll */
	.chat {
		--chat-below: 0px;

		display: flex;
		gap: var(--nebu-space-5);
		height: calc(100dvh - var(--nebu-top, 60px) - 1px - var(--nebu-pad-top) - var(--nebu-pad-bottom) - var(--chat-below));
		min-height: 0;
	}
	.chat.mobile {
		--chat-below: 56px;
	}
	.col {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.side {
		flex: 0 0 280px;
	}
	.main {
		flex: 1 1 auto;
	}
	.settings {
		flex: 0 0 320px;
	}
	.scroll {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		height: 0;
		overflow-x: hidden;
		overflow-y: auto;
	}
	.thread,
	.form {
		padding: 0 var(--nebu-card-pad);
	}
	.form {
		padding-bottom: var(--nebu-space-3);
	}
	.flow {
		display: flex;
		flex: 1 0 auto;
		flex-direction: column;
		min-width: 0;
	}
	.centred .flow {
		justify-content: center;
	}
	.model {
		display: flex;
		flex: 1 1 0;
		min-width: 0;
		max-width: 20rem;
	}
	.sheetsearch {
		display: flex;
		min-width: 0;
	}
	.sheetlist {
		display: flex;
		flex-direction: column;
		min-width: 0;
		margin: var(--nebu-space-3) calc(-1 * var(--nebu-sheet-pad)) 0;
		--nebu-row-inset: var(--nebu-sheet-pad);
	}
	.composer {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		gap: var(--nebu-space-3);
		min-width: 0;
	}
	.pills {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-1);
	}
	.line {
		display: flex;
		gap: var(--nebu-space-2);
		align-items: flex-end;
		min-width: 0;
	}
	.box {
		flex: 1 1 auto;
		min-width: 0;
	}
	.beside {
		display: inline-flex;
		flex: 0 0 auto;
		align-items: center;
		min-height: var(--nebu-input-height);
	}
	.progress {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		padding: var(--nebu-space-3) 0;
	}
	.track {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		max-width: 32rem;
	}
	.bar {
		display: flex;
		flex: 1 1 auto;
		min-width: 0;
	}
	.tiles {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-2);
	}
</style>
