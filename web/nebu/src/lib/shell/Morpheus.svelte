<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { onMount } from 'svelte';
	import { innerWidth } from 'svelte/reactivity/window';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoMenu, type NeoMenuItem } from '#lib/neo/floating/menu/index.js';
	import { NeoIconWatch } from '#lib/neo/icons/index.js';
	import { NeoTextarea } from '#lib/neo/inputs/index.js';
	import { listModels } from '#lib/api/gateway.js';
	import { reason } from '#lib/api/transport.js';
	import { session as account } from '#lib/auth/session.svelte.js';
	import { num, pct } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { modelMode, modelOptions, type PendingCall } from '#lib/pages/chat/chat.svelte.js';
	import { chooseEffort, effortDefault, effortOf, effortOptions } from '#lib/pages/chat/effort.svelte.js';
	import Turn from '#lib/pages/chat/Turn.svelte';
	import { launchEntry } from '#lib/pages/serve/serve.js';
	import { ChatTurnSchema } from '#lib/proto/nebu/v1/chat_pb.js';
	import { morpheus, span } from '#lib/shell/morpheus.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { prefs } from '#lib/state/prefs.svelte.js';
	import Dot from '#lib/ui/Dot.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import { hatch } from '#lib/ui/motion.svelte.js';
	import { taskLamp } from '#lib/ui/status.js';
	import ArrowUpIcon from 'phosphor-svelte/lib/ArrowUpIcon';
	import BrainIcon from 'phosphor-svelte/lib/BrainIcon';
	import CaretDownIcon from 'phosphor-svelte/lib/CaretDownIcon';
	import CheckIcon from 'phosphor-svelte/lib/CheckIcon';
	import HandPalmIcon from 'phosphor-svelte/lib/HandPalmIcon';
	import PlayIcon from 'phosphor-svelte/lib/PlayIcon';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import StopIcon from 'phosphor-svelte/lib/StopIcon';

	// The assistant's panel, a column on the right edge where the shell has the room and a sheet over the page where it has not
	let { docked, room }: { docked: boolean; room: number } = $props();

	const session = morpheus.session;
	let scroller = $state<HTMLDivElement>();
	let flow = $state<HTMLDivElement>();
	let box = $state<HTMLDivElement>();
	// Whether the thread follows its newest words, off once the person scrolls up to read
	let pinned = $state(true);
	let held = $state(false);

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
	const share = $derived(contextMax > 0 ? Math.min(1, contextUsed / contextMax) : 0);
	const placeholder = $derived(morpheus.loaded && session.model === '' ? 'No models running' : 'Choose a model');

	const most = $derived(Math.max(span.min, docked ? room : Math.floor((innerWidth.current ?? 0) * 0.96)));
	const width = $derived(fit(morpheus.width));

	const levels = $derived(effortOptions(morpheus.entry));
	const effort = $derived(effortOf(morpheus.entry));
	const effortName = $derived(levels.find((o) => o.value === effort)?.label ?? 'Not set');
	const efforts = $derived<NeoMenuItem[]>([
		...levels.map((o) => ({ value: o.value, label: o.label, description: o.description, color: o.value === effort ? 'primary' : undefined, after: o.value === effort ? tick : undefined })),
		...(effort && effortDefault(morpheus.entry) === '' ? [{ value: '', label: 'Not set', divider: true }] : [])
	]);

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

	$effect(() => {
		if (!held) return;
		const root = document.documentElement;
		root.classList.add('nebu-resizing');
		return () => root.classList.remove('nebu-resizing');
	});

	function fit(px: number): number {
		return Math.round(Math.min(Math.max(px, span.min), most));
	}

	function grab(e: PointerEvent) {
		if (e.button !== 0) return;
		e.preventDefault();
		const from = e.clientX;
		const start = width;
		let next = start;
		let frame = 0;
		let moved = false;
		const move = (m: PointerEvent) => {
			moved = true;
			next = fit(start + from - m.clientX);
			frame ||= requestAnimationFrame(() => {
				frame = 0;
				morpheus.width = next;
			});
		};
		const end = () => {
			cancelAnimationFrame(frame);
			if (moved) morpheus.width = next;
			held = false;
			window.removeEventListener('pointermove', move);
			window.removeEventListener('pointerup', end);
			window.removeEventListener('pointercancel', end);
		};
		(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
		window.addEventListener('pointermove', move);
		window.addEventListener('pointerup', end);
		window.addEventListener('pointercancel', end);
		held = true;
	}

	function nudge(e: KeyboardEvent) {
		const step = e.shiftKey ? 64 : 16;
		const to = { ArrowLeft: width + step, ArrowRight: width - step, Home: span.min, End: most }[e.key];
		if (to === undefined) return;
		e.preventDefault();
		morpheus.width = fit(to);
	}

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

	function pickEffort(item: NeoMenuItem) {
		if (morpheus.entry) chooseEffort(morpheus.entry, (item.value as string) || undefined);
	}
</script>

{#snippet tick()}<CheckIcon size={16} />{/snippet}

{#snippet grip()}
	<!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
	<div class="grip" class:held class:inside={!docked} role="separator" aria-orientation="vertical" aria-label="Resize Morpheus" aria-valuemin={span.min} aria-valuemax={most} aria-valuenow={width} tabindex="0" title="Drag to resize, double-click to reset" onpointerdown={grab} ondblclick={() => (morpheus.width = 0)} onkeydown={nudge}></div>
{/snippet}

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
			<p class="note unmeasured"><Dot lamp={taskLamp(ended)} /><span>{morpheus.noteLine(turn)}</span></p>
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
			<PanelHeader title="Morpheus" subtitle={morpheus.looking} onclose={() => (morpheus.open = false)}>
				{#snippet tools()}
					<span class="model">
						<span class="sizer" aria-hidden="true">{session.model || placeholder}</span>
						<Picker bind:value={() => session.model || undefined, (v) => morpheus.pick(v ?? '')} fetch={fetchModels} label="Model" {placeholder} compact disabled={morpheus.loaded && session.model === ''} current={session.model} />
					</span>
					<NeoButton text aria-label="New chat" title="New chat" disabled={shown.length === 0} onclick={() => morpheus.reset()}>{#snippet icon()}<PlusIcon size={20} />{/snippet}</NeoButton>
				{/snippet}
			</PanelHeader>
		</div>
		<div class="thread" class:centred={quiet} bind:this={scroller} {onscroll}>
			<div class="flow" bind:this={flow}>{@render messages()}</div>
		</div>
		<div class="composer">
			<!-- svelte-ignore a11y_no_static_element_interactions -->
			<div class="box" bind:this={box} {onkeydown}>
				<NeoTextarea bind:value={() => morpheus.text, (v) => (morpheus.text = String(v ?? ''))} placeholder={session.model === '' ? 'No models running' : 'Message Morpheus'} aria-label="Message Morpheus" rows={1} autoResize={{ min: 1, max: 8 }} disabled={session.model === ''} elevation={0} hover={0} borderless placement="inside" floating={false} width="100%" resize="none" />
			</div>
			<div class="controls">
				<Tip text={prefs.morpheusAsks ? 'Ask before changes: Morpheus waits for you to run or skip a call that changes something' : 'Morpheus runs calls that change something without asking first'}>
					<NeoButton text class="chip" aria-label="Ask before changes" aria-pressed={prefs.morpheusAsks} color={prefs.morpheusAsks ? 'primary' : undefined} onclick={() => (prefs.morpheusAsks = !prefs.morpheusAsks)}>
						{#snippet icon()}<HandPalmIcon size={16} weight={prefs.morpheusAsks ? 'fill' : 'regular'} />{/snippet}<span class="word">Ask</span>
					</NeoButton>
				</Tip>
				{#if levels.length > 0}
					<NeoMenu items={efforts} onSelect={pickEffort} placement="top-start" filled>
						<NeoButton text class="chip" aria-label="Reasoning effort: {effortName}" title="Reasoning effort">
							{#snippet icon()}<BrainIcon size={16} />{/snippet}<span class="word">{effortName}</span><span class="caret"><CaretDownIcon size={12} weight="bold" /></span>
						</NeoButton>
					</NeoMenu>
				{/if}
				{#if contextUsed > 0 && contextMax > 0}
					<Tip text="{num(contextUsed)} of {num(contextMax)} context tokens used">
						<span class="gauge" class:full={share > 0.85} role="meter" aria-label="Context used" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(share * 100)} aria-valuetext="{pct(share * 100)}, {num(contextUsed)} of {num(contextMax)} tokens">
							<svg viewBox="0 0 20 20" aria-hidden="true"><circle class="track" cx="10" cy="10" r="7.5" /><circle class="arc" cx="10" cy="10" r="7.5" pathLength="100" stroke-dasharray="{Math.max(share * 100, 2)} 100" /></svg>
							<span>{pct(share * 100)}</span>
						</span>
					</Tip>
				{/if}
				<span class="send">
					{#if session.busy}
						<NeoButton elevation={2} rounded aria-label="Stop" title="Stop" onclick={() => session.stop()}>{#snippet icon()}<StopIcon size={16} weight="fill" />{/snippet}</NeoButton>
					{:else}
						<NeoButton class="primary" elevation={2} rounded aria-label="Send" title="Send" disabled={!canSend} onclick={() => send()}>{#snippet icon()}<ArrowUpIcon size={16} weight="bold" />{/snippet}</NeoButton>
					{/if}
				</span>
			</div>
		</div>
	</div>
{/snippet}

{#if !docked}
	<Sheet bind:open={() => morpheus.open, (v) => (morpheus.open = v)} {width} edge={grip}>{@render panel()}</Sheet>
{:else if morpheus.open}
	<aside class="column" aria-label="Morpheus" style:flex-basis="{width}px" style:width="{width}px" in:hatch>{@render grip()}{@render panel()}</aside>
{/if}

<style>
	/* The rail mirrored: a column on the right edge the page narrows for */
	.column {
		position: sticky;
		top: 0;
		display: flex;
		flex: 0 0 auto;
		flex-direction: column;
		align-self: flex-start;
		height: 100dvh;
		background: var(--nebu-rail);
		border-left: 1px solid var(--nebu-border);
	}
	.grip {
		position: absolute;
		top: 0;
		bottom: 0;
		left: -5px;
		z-index: 2;
		width: 9px;
		cursor: col-resize;
		outline: none;
		touch-action: none;
	}
	.grip.inside {
		left: 0;
		width: 7px;
	}
	.grip::after {
		position: absolute;
		top: 0;
		bottom: 0;
		left: 4px;
		width: 1px;
		background: var(--neo-color-primary);
		opacity: 0;
		content: '';
		transition:
			opacity var(--nebu-fast) var(--nebu-ease),
			width var(--nebu-fast) var(--nebu-ease),
			left var(--nebu-fast) var(--nebu-ease);
	}
	.grip.inside::after {
		left: 0;
	}
	.grip:hover::after,
	.grip:focus-visible::after,
	.grip.held::after {
		left: 3px;
		width: 3px;
		opacity: 0.7;
		transition-delay: 80ms;
	}
	.grip.inside:hover::after,
	.grip.inside:focus-visible::after,
	.grip.inside.held::after {
		left: 0;
	}
	.grip.held::after {
		opacity: 1;
		transition-delay: 0ms;
	}
	:global(html.nebu-resizing),
	:global(html.nebu-resizing *) {
		cursor: col-resize !important;
		user-select: none !important;
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
	.bar :global(.head > .titles) {
		flex: 1 1 0;
		min-width: auto;
	}
	.bar :global(.head > .fill) {
		display: none;
	}
	.bar :global(.head .title) {
		white-space: nowrap;
	}
	.bar :global(.head .under) {
		overflow: hidden;
		white-space: nowrap;
		text-overflow: ellipsis;
		contain: inline-size;
	}
	.model {
		display: grid;
		flex: 0 1 auto;
		min-width: 0;
	}
	.model > :global(*) {
		grid-area: 1 / 1;
		min-width: 0;
	}
	.sizer {
		height: 0;
		padding: 0 50px 0 15px;
		overflow: hidden;
		white-space: nowrap;
		visibility: hidden;
	}
	.model :global(.inlabel) {
		display: none;
	}
	.model :global(.neo-input-before) {
		padding-right: 0;
	}
	.model :global(.neo-input-group) {
		min-width: 0;
	}
	.model :global(.neo-input:has(> .neo-input-display-content)) {
		display: flex;
		flex: 1 1 auto;
		min-width: 0;
	}
	.model :global(.neo-input-display-content) {
		flex: 1 1 0;
		width: 0;
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
	.composer {
		display: flex;
		flex: 0 0 auto;
		flex-direction: column;
		min-width: 0;
		padding: 2px var(--nebu-space-2) var(--nebu-space-2);
		background: var(--neo-background-color);
		border-radius: 18px;
		box-shadow: var(--neo-box-shadow-inset-1);
		transition: box-shadow var(--nebu-normal) var(--nebu-ease);
	}
	.composer:has(:global(textarea:focus)) {
		box-shadow: var(--neo-box-shadow-inset-2);
	}
	.box {
		min-width: 0;
	}
	.box :global(.neo-textarea-group) {
		margin: 0;
		background: transparent;
	}
	.box :global(.neo-textarea) {
		padding: 10px 6px 6px;
	}
	.controls {
		display: flex;
		gap: 2px;
		align-items: center;
		min-width: 0;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
	}
	.controls :global(.neo-button.chip) {
		--neo-btn-padding: 0.25rem 0.5rem;
		--neo-btn-min-height: 30px;

		gap: 5px;
		font-size: var(--nebu-text-secondary);
		white-space: nowrap;
	}
	.controls :global(.neo-button.chip:not([aria-pressed='true'])) {
		color: var(--nebu-text-muted);
	}
	.caret {
		display: inline-flex;
		margin-left: 2px;
	}
	.gauge {
		display: inline-flex;
		gap: 5px;
		align-items: center;
		padding: 0 var(--nebu-space-2);
		font-size: var(--nebu-text-caption);
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
	.gauge svg {
		width: 16px;
		height: 16px;
		transform: rotate(-90deg);
	}
	.gauge circle {
		fill: none;
		stroke-width: 2.5;
	}
	.track {
		stroke: color-mix(in srgb, var(--nebu-text-muted) 22%, transparent);
	}
	.arc {
		stroke: var(--neo-color-primary);
		stroke-linecap: round;
	}
	.full .arc {
		stroke: var(--neo-color-warning);
	}
	.send {
		display: inline-flex;
		flex: 0 0 auto;
		margin-left: auto;
		--neo-btn-padding-empty: 0.4375rem;
	}
</style>
