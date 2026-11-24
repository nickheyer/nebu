<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { onMount } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoIconWatch } from '#lib/neo/icons/index.js';
	import { NeoProgressBar } from '#lib/neo/progress/index.js';
	import { listModels } from '#lib/api/gateway.js';
	import { reason } from '#lib/api/transport.js';
	import { session as account } from '#lib/auth/session.svelte.js';
	import { num, pct } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { modelMode, modelOptions, type PendingCall } from '#lib/pages/chat/chat.svelte.js';
	import Turn from '#lib/pages/chat/Turn.svelte';
	import { launchEntry } from '#lib/pages/serve/serve.js';
	import { ChatTurnSchema } from '#lib/proto/nebu/v1/chat_pb.js';
	import { morpheus } from '#lib/shell/morpheus.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { prefs } from '#lib/state/prefs.svelte.js';
	import Dot from '#lib/ui/Dot.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Textarea from '#lib/ui/fields/Textarea.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import { hatch } from '#lib/ui/motion.svelte.js';
	import { taskLamp } from '#lib/ui/status.js';
	import PaperPlaneRightIcon from 'phosphor-svelte/lib/PaperPlaneRightIcon';
	import PlayIcon from 'phosphor-svelte/lib/PlayIcon';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import StopIcon from 'phosphor-svelte/lib/StopIcon';

	// The assistant's panel, a column on the right edge where the shell has the room and a sheet over the page where it has not
	let { docked }: { docked: boolean } = $props();

	const session = morpheus.session;
	let scroller = $state<HTMLDivElement>();
	let flow = $state<HTMLDivElement>();
	let box = $state<HTMLDivElement>();
	// Whether the thread follows its newest words, off once the person scrolls up to read
	let pinned = $state(true);

	const shown = $derived(session.turns.filter((t) => t.role !== 'tool'));
	const lastAssistant = $derived(session.turns.findLast((t) => t.role === 'assistant'));
	const quiet = $derived(shown.length === 0 && !session.busy);
	// The model is writing unless the session is busy running a call it made
	const writing = $derived(session.busy && session.pending.length === 0);
	const idle = $derived(!session.busy && session.model !== '');
	const canSend = $derived(morpheus.text.trim() !== '' && idle);
	const draftTurn = $derived(create(ChatTurnSchema, { role: 'assistant', text: session.draft }));
	const userName = $derived(account.displayName || 'You');
	// Tokens the last answer left in the context and the most the route holds, both known only after an answer
	const contextUsed = $derived(lastAssistant ? lastAssistant.promptTokens + lastAssistant.completionTokens : 0);
	const contextMax = $derived(morpheus.models.find((m) => m.id === session.model)?.inventory.context_length ?? 0);

	$effect(() => {
		if (morpheus.open) void morpheus.start();
	});

	// Control or command with J opens and closes the panel from any page that offers it
	onMount(() => {
		const handler = (e: KeyboardEvent) => {
			if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'j') {
				e.preventDefault();
				morpheus.open = !morpheus.open;
			}
		};
		window.addEventListener('keydown', handler);
		return () => window.removeEventListener('keydown', handler);
	});

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

	async function fetchModels(q: string, _cursor: string, _limit: number, signal: AbortSignal) {
		const models = await listModels(signal);
		return { items: modelOptions(models.filter((m) => modelMode(m) === 'text'), q), next: '' };
	}

	async function attempt(title: string, step: () => Promise<void>) {
		pinned = true;
		try {
			await step();
		} catch (err) {
			toast.error(title, reason(err));
		}
	}

	function send(prompt = morpheus.text.trim()) {
		if (prompt === '' || !idle) return;
		morpheus.text = '';
		box?.querySelector('textarea')?.focus();
		void attempt('Send failed', () => morpheus.send(prompt));
	}

	// Enter sends, shift and enter starts a new line
	function onkeydown(e: KeyboardEvent) {
		if (e.key !== 'Enter' || e.shiftKey || e.isComposing) return;
		e.preventDefault();
		send();
	}
</script>

{#snippet messages()}
	{#if quiet && !morpheus.loaded}
		<Skeleton kind="text" rows={2} />
	{:else if quiet && morpheus.failure}
		<Empty icon={PlayIcon} text="Morpheus failed to load" hint={morpheus.failure} action="Retry" onclick={() => morpheus.retry()} />
	{:else if quiet && session.model === ''}
		<Empty icon={PlayIcon} text="No models are running" hint="Morpheus needs a running model" action={launchEntry(live).long} href={launchEntry(live).href} primary />
	{:else if quiet}
		<div class="hero">
			<span class="orb" aria-hidden="true"><NeoIconWatch size="36px" enter={false} /></span>
			{#each morpheus.asks as ask (ask)}
				<NeoButton text onclick={() => send(ask)}>{ask}</NeoButton>
			{/each}
		</div>
	{/if}
	{#each shown as turn (turn.id || turn)}
		{@const ended = morpheus.note(turn)}
		{#if ended !== undefined}
			<p class="note unmeasured"><Dot lamp={taskLamp(ended)} /><span>{turn.text}</span></p>
		{:else}
			<Turn
				{turn}
				{session}
				name={turn.role === 'user' ? userName : 'Morpheus'}
				traceLinks
				pending={turn === lastAssistant ? morpheus.waiting : []}
				onretry={turn === lastAssistant && idle ? () => attempt('Ask again failed', () => morpheus.again()) : undefined}
				onrun={(c: PendingCall) => attempt('Tool call failed', () => morpheus.run(c))}
				onskip={(c: PendingCall) => attempt('Tool call failed', () => morpheus.skip(c))}
			/>
		{/if}
	{/each}
	{#if writing}
		<Turn turn={draftTurn} {session} name="Morpheus" streaming />
	{/if}
{/snippet}

{#snippet panel()}
	<div class="morpheus" class:docked>
		<div class="bar dense">
			<PanelHeader title="Morpheus" onclose={() => (morpheus.open = false)}>
				{#snippet tools()}
					<span class="model">
						<Picker bind:value={() => session.model || undefined, (v) => morpheus.pick(v ?? '')} fetch={fetchModels} label="Model" placeholder={morpheus.loaded && session.model === '' ? 'No models running' : 'Choose a model'} compact disabled={morpheus.loaded && session.model === ''} current={session.model} />
					</span>
					<NeoButton text aria-label="New chat" title="New chat" disabled={shown.length === 0} onclick={() => morpheus.reset()}>{#snippet icon()}<PlusIcon size={20} />{/snippet}</NeoButton>
				{/snippet}
			</PanelHeader>
		</div>
		<div class="looking"><Str value={morpheus.looking} kind="text" color="muted" clip width="100%" /></div>
		<div class="thread" class:centred={quiet} bind:this={scroller} {onscroll}>
			<div class="flow" bind:this={flow}>{@render messages()}</div>
		</div>
		<div class="dock">
			<div class="line">
				<!-- svelte-ignore a11y_no_static_element_interactions -->
				<div class="box" bind:this={box} {onkeydown}>
					<Textarea bind:value={() => morpheus.text, (v) => (morpheus.text = v)} placeholder={session.model === '' ? 'No models running' : 'Message Morpheus'} rows={1} maxRows={8} disabled={session.model === ''} />
				</div>
				<span class="beside">
					{#if session.busy}
						<NeoButton elevation={3} rounded aria-label="Stop" title="Stop" onclick={() => session.stop()}>{#snippet icon()}<StopIcon size={20} weight="fill" />{/snippet}</NeoButton>
					{:else}
						<NeoButton class="primary" elevation={3} rounded aria-label="Send" title="Send" disabled={!canSend} onclick={() => send()}>{#snippet icon()}<PaperPlaneRightIcon size={20} weight="fill" />{/snippet}</NeoButton>
					{/if}
				</span>
			</div>
			<div class="under">
				<Switch bind:checked={() => prefs.morpheusAsks, (v) => (prefs.morpheusAsks = v)} label="Ask before changes" compact />
				{#if contextUsed > 0 && contextMax > 0}
					<span class="gauge" title="{num(contextUsed)} of {num(contextMax)} context tokens used">
						<span class="meter"><NeoProgressBar value={Math.min(100, (contextUsed / contextMax) * 100)} elevation={-1} height="6px" width="100%" color={contextUsed / contextMax > 0.85 ? 'warning' : 'primary'} immediate /></span>
						<span>{pct((contextUsed / contextMax) * 100)} of context</span>
					</span>
				{/if}
			</div>
		</div>
	</div>
{/snippet}

{#if !docked}
	<Sheet bind:open={() => morpheus.open, (v) => (morpheus.open = v)} width={400}>{@render panel()}</Sheet>
{:else if morpheus.open}
	<aside class="column" aria-label="Morpheus" in:hatch>{@render panel()}</aside>
{/if}

<style>
	/* The rail mirrored: a column on the right edge the page narrows for */
	.column {
		position: sticky;
		top: 0;
		display: flex;
		flex: 0 0 400px;
		flex-direction: column;
		align-self: flex-start;
		width: 400px;
		height: 100dvh;
		background: var(--nebu-rail);
		border-left: 1px solid var(--nebu-border);
	}
	.morpheus {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		gap: var(--nebu-space-3);
		min-width: 0;
		min-height: 0;
	}
	/* The column keeps the rail's own padding, and a sheet gives up its own down to the same */
	.docked {
		padding: 0 var(--nebu-space-3) var(--nebu-space-3);
	}
	.morpheus:not(.docked) {
		margin: 0 calc(var(--nebu-space-3) - var(--nebu-sheet-pad) - var(--nebu-scroll-pad)) 0 calc(var(--nebu-space-3) - var(--nebu-sheet-pad));
	}
	/* The docked head is as tall as the top bar, whose rule it carries on to the window's edge */
	.docked .bar {
		flex: 0 0 auto;
		height: calc(var(--nebu-top, 60px) + 1px);
		margin: 0 calc(-1 * var(--nebu-space-3));
		padding: 0 var(--nebu-space-3);
		border-bottom: 1px solid var(--nebu-border);
	}
	.docked .bar :global(.head) {
		height: 60px;
		margin-bottom: 0;
	}
	.model {
		display: flex;
		flex: 1 1 10rem;
		min-width: 0;
		max-width: 14rem;
	}
	.model > :global(*) {
		flex: 1 1 auto;
		min-width: 0;
	}
	.looking {
		display: flex;
		flex: 0 0 auto;
		min-width: 0;
		font-size: var(--nebu-text-secondary);
	}
	.thread {
		display: flex;
		flex: 1 1 auto;
		flex-direction: column;
		height: 0;
		margin: 0 calc(-1 * var(--nebu-space-2));
		padding: 0 var(--nebu-space-2);
		overflow-x: hidden;
		overflow-y: auto;
	}
	.flow {
		display: flex;
		flex: 1 0 auto;
		flex-direction: column;
		gap: var(--nebu-space-5);
		min-width: 0;
		padding: var(--nebu-space-2) 0 var(--nebu-space-4);
	}
	.centred .flow {
		justify-content: center;
	}
	/* An answer and the calls in it give up padding the column cannot spare, so a waiting call holds its name, Skip and Run on one line */
	.flow :global(.turn:not(.user) .body) {
		padding: var(--nebu-space-3) var(--nebu-space-3) var(--nebu-space-3) 22px;
	}
	.flow :global(.calls) {
		--nebu-row-inset: var(--nebu-space-2);
	}
	.flow :global(.calls .listrow.flex) {
		gap: var(--nebu-space-2);
	}
	.flow :global(.calls .listrow.flex .fact),
	.flow :global(.calls .listrow.flex .primary) {
		min-width: 0;
	}
	.flow :global(.calls .action) {
		--neo-btn-padding: 0.25rem 0.5rem;
	}
	/* A name the row cannot hold is cut, the whole of it on hover */
	.flow :global(.calls .primary .str) {
		display: block;
		overflow: hidden;
		white-space: nowrap;
		text-overflow: ellipsis;
	}
	.hero {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-1);
		align-items: center;
		padding: var(--nebu-space-6) 0;
		text-align: center;
	}
	.orb {
		display: grid;
		place-items: center;
		width: 88px;
		height: 88px;
		margin-bottom: var(--nebu-space-4);
		color: var(--neo-color-primary);
		background: var(--neo-background-color);
		border-radius: 50%;
		box-shadow: var(--neo-box-shadow-convex-4), var(--nebu-machined);
	}
	/* What the panel told the model of a task is a line of the thread, not a message of the person's */
	.note {
		display: flex;
		gap: var(--nebu-space-2);
		align-items: center;
		min-width: 0;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		overflow-wrap: anywhere;
	}
	.dock {
		display: flex;
		flex: 0 0 auto;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
		padding: var(--nebu-space-3) var(--nebu-space-4);
		background: var(--neo-background-color);
		border-radius: 20px;
		box-shadow: var(--neo-box-shadow-raised-3), var(--nebu-machined);
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
	.box :global(.neo-textarea-group) {
		margin: 0;
	}
	.beside {
		display: inline-flex;
		flex: 0 0 auto;
		align-items: center;
		min-height: var(--nebu-input-height);
	}
	.under {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-1) var(--nebu-space-4);
		align-items: center;
		justify-content: space-between;
		min-height: 20px;
		font-size: var(--nebu-text-caption);
		color: var(--nebu-text-muted);
	}
	.gauge {
		display: inline-flex;
		gap: var(--nebu-space-2);
		align-items: center;
		margin-left: auto;
		font-variant-numeric: tabular-nums;
	}
	.meter {
		display: inline-flex;
		width: 72px;
	}
</style>
