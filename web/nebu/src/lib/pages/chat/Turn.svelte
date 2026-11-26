<script lang="ts">
	import { tick, untrack, type Snippet } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoTextarea } from '#lib/neo/inputs/index.js';
	import { NeoSkeletonText } from '#lib/neo/skeletons/index.js';
	import { date, ms, num, plural, shortId, tokensPerSecond } from '#lib/format.js';
	import type { ChatTurn } from '#lib/proto/nebu/v1/chat_pb.js';
	import type { TraceToolCall } from '#lib/proto/nebu/v1/gateway_pb.js';
	import type { ChatSession, PendingCall } from '#lib/pages/chat/chat.svelte.js';
	import TurnAction from '#lib/pages/chat/TurnAction.svelte';
	import { voice } from '#lib/pages/chat/voice.svelte.js';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import KeyRows, { type KeyRow } from '#lib/ui/KeyRows.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import Markdown from '#lib/ui/Markdown.svelte';
	import Pop from '#lib/ui/Pop.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import type { DotShape, Lamp } from '#lib/ui/status.js';
	import ArrowsClockwiseIcon from 'phosphor-svelte/lib/ArrowsClockwiseIcon';
	import PencilSimpleIcon from 'phosphor-svelte/lib/PencilSimpleIcon';
	import SparkleIcon from 'phosphor-svelte/lib/SparkleIcon';
	import SpeakerHighIcon from 'phosphor-svelte/lib/SpeakerHighIcon';
	import StopIcon from 'phosphor-svelte/lib/StopIcon';
	import TrashIcon from 'phosphor-svelte/lib/TrashIcon';

	// One message of the thread, what the person typed pressed into the surface and what the model wrote raised from it, with the ways to change it under it
	let {
		turn,
		session,
		name,
		pending = [],
		streaming = false,
		traceLinks = false,
		// Messages that follow this one in the thread, which a rewrite of it removes
		later = 0,
		onedit,
		onretry,
		ondelete,
		onrun,
		onskip,
		children
	}: {
		turn: ChatTurn;
		session: ChatSession;
		name: string;
		pending?: PendingCall[];
		streaming?: boolean;
		traceLinks?: boolean;
		later?: number;
		onedit?: (text: string) => void;
		onretry?: () => void;
		ondelete?: () => void;
		onrun?: (c: PendingCall) => void;
		onskip?: (c: PendingCall) => void;
		children?: Snippet;
	} = $props();

	const at = (ts: ChatTurn['startedAt']) => date(ts)?.getTime() ?? 0;
	const user = $derived(turn.role === 'user');
	// An answer that ended within the last moments dims its lamp from lit instead of appearing dim
	const fresh = untrack(() => turn.role === 'assistant' && !streaming && Date.now() - at(turn.finishedAt) < 2000);
	const ttft = $derived(turn.firstTokenAt && turn.startedAt ? at(turn.firstTokenAt) - at(turn.startedAt) : undefined);
	const tps = $derived.by(() => {
		if (!turn.firstTokenAt || !turn.finishedAt || turn.completionTokens < 2) return undefined;
		const span = at(turn.finishedAt) - at(turn.firstTokenAt);
		return span > 0 ? ((turn.completionTokens - 1) / span) * 1000 : undefined;
	});
	const stats = $derived<KeyRow[]>([
		{ label: 'Time to first token', value: ttft === undefined ? '' : ms(ttft) },
		{ label: 'Speed', value: tps === undefined ? '' : tokensPerSecond(tps) },
		{ label: 'Prompt', value: turn.promptTokens ? plural(turn.promptTokens, 'token') : '' },
		{ label: 'Answer', value: turn.completionTokens ? plural(turn.completionTokens, 'token') : '' },
		{ label: 'Ended on', value: turn.stop },
		traceLinks
			? { label: 'Trace', value: shortId(turn.traceId), tip: turn.traceId, href: `/traffic/${turn.traceId}`, mono: true }
			: { label: 'Trace', value: turn.traceId, mono: true, copy: true }
	]);
	// The one reading worth a glance, the speed when it was measured and the size of the answer otherwise
	const reading = $derived(tps !== undefined ? tokensPerSecond(tps) : turn.completionTokens ? plural(turn.completionTokens, 'token') : '');
	const initial = $derived((name.trim()[0] ?? '?').toUpperCase());
	const editable = $derived(onedit !== undefined && turn.media.length === 0);
	const aloud = $derived(turn.role === 'assistant' && turn.text !== '' && voice.can);
	const speaking = $derived(aloud && voice.playing === turn.id);
	// Messages asking again from this one removes beyond the one answer it writes anew
	const beyond = $derived(Math.max(0, user ? later - 1 : later));

	let editing = $state(false);
	let draft = $state('');
	let box = $state<HTMLDivElement>();

	async function startEdit() {
		draft = turn.text;
		editing = true;
		await tick();
		const area = box?.querySelector('textarea');
		area?.focus();
		area?.setSelectionRange(draft.length, draft.length);
	}

	function saveEdit() {
		if (draft.trim() === '' || (draft === turn.text && !user)) return;
		editing = false;
		onedit?.(draft);
	}

	// Escape leaves the edit, enter with the control or command key saves it
	function editKey(e: KeyboardEvent) {
		if (e.key === 'Escape') {
			e.preventDefault();
			editing = false;
		} else if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
			e.preventDefault();
			saveEdit();
		}
	}

	function lampOf(c: TraceToolCall, waiting: boolean): { lamp: Lamp; shape: DotShape; word: string } {
		if (waiting) return { lamp: 'warning', shape: 'half', word: 'waiting' };
		if (c.failed) return { lamp: 'error', shape: 'filled', word: 'failed' };
		if (c.result) return { lamp: 'success', shape: 'filled', word: 'done' };
		return { lamp: 'muted', shape: 'hollow', word: 'running' };
	}

	function pretty(s: string): string {
		try {
			return JSON.stringify(JSON.parse(s), null, 2);
		} catch {
			return s;
		}
	}
</script>

{#snippet statsView()}
	<KeyRows dense rows={stats} />
{/snippet}

<article class="turn" class:user class:streaming class:fresh class:failed={turn.error !== ''} aria-label="{name}, {user ? 'you' : 'the model'}">
	<header class="meta">
		<span class="coin" aria-hidden="true">{#if user}{initial}{:else}<SparkleIcon size={14} weight="fill" />{/if}</span>
		<Str value={name} weight={500} />
		{#if streaming}<span class="state">writing</span>{/if}
	</header>
	{#if turn.images.length}
		<Row gap={8} justify={user ? 'flex-end' : 'flex-start'}>
			{#each turn.images as f, i (i)}
				{@const url = session.fileUrl(f)}
				{@const audio = f.mediaType.startsWith('audio/')}
				{#if url && audio}
					<audio class="clip" src={url} controls title={f.name}></audio>
				{:else if url}
					<img class="thumb" src={url} alt={f.name} />
				{:else}
					<NeoSkeletonText lines={1} width={audio ? '280px' : '160px'} height={audio ? '40px' : '160px'} />
				{/if}
			{/each}
		</Row>
	{/if}
	<div class="body" class:editing class:bare={user && !editing && !turn.text && !turn.error}>
		{#if !user}<span class="lamp" aria-hidden="true"></span>{/if}
		{#if editing}
			<!-- svelte-ignore a11y_no_static_element_interactions -->
			<div class="editor" bind:this={box} onkeydown={editKey}>
				<NeoTextarea bind:value={draft} rows={2} elevation={-1} placement="inside" floating={false} width="100%" resize="none" autoResize={{ min: 2, max: 16 }} aria-label="Message text" />
				<div class="editfoot">
					<span class="secondary note">
						{#if later > 0 && user}Saving removes the {plural(later, 'message')} after this one and asks the model again{:else if later > 0}Saving removes the {plural(later, 'message')} after this one{:else if user}Saving asks the model again{/if}
					</span>
					<NeoButton text onclick={() => (editing = false)}>Cancel</NeoButton>
					<NeoButton class="primary" elevation={2} disabled={draft.trim() === '' || (draft === turn.text && !user)} onclick={saveEdit}>{user ? 'Save and ask again' : 'Save'}</NeoButton>
				</div>
			</div>
		{:else}
			{#if children}
				{@render children()}
			{:else if turn.text}
				<Markdown source={turn.text} />
			{/if}
			{#if turn.media.length}
				<Row gap={8}>
					{#each turn.media as f, i (i)}
						{@const url = session.fileUrl(f)}
						{#if f.error}
							<Str value={f.error} kind="message" color="error" />
						{:else if !url}
							<NeoSkeletonText lines={1} width={f.mediaType.startsWith('audio/') ? '280px' : '256px'} height={f.mediaType.startsWith('audio/') ? '40px' : '256px'} />
						{:else if f.mediaType.startsWith('video/')}
							<video class="video" src={url} controls><track kind="captions" /></video>
						{:else if f.mediaType.startsWith('audio/')}
							<audio class="clip" src={url} controls title={f.name}></audio>
						{:else}
							<img class="made" src={url} alt={f.name} />
						{/if}
					{/each}
				</Row>
			{/if}
			{#if turn.toolCalls.length}
				<div class="calls">
					{#each turn.toolCalls as c (c.id)}
						{@const p = pending.find((x) => x.id === c.id)}
						{@const state = lampOf(c, p !== undefined)}
						{#snippet callBody()}
							<Stack gap={4}>
								<span class="label">Arguments</span>
								<Screen padding={8} maxHeight="16rem"><div class="lines">{pretty(c.arguments)}</div></Screen>
								{#if c.result}
									<span class="label">Result</span>
									<Screen padding={8} maxHeight="16rem"><div class="lines">{pretty(c.result)}</div></Screen>
								{/if}
							</Stack>
						{/snippet}
						{#snippet runSkip()}
							{#if p}
								<NeoButton text onclick={() => onskip?.(p)}>Skip</NeoButton>
								<NeoButton text onclick={() => onrun?.(p)}>Run</NeoButton>
							{/if}
						{/snippet}
						<ListRow
							lamp={state.lamp}
							shape={state.shape}
							primary={c.name}
							primaryTip={[c.name, c.server].filter(Boolean).join('\n')}
							facts={[{ value: state.word }, { value: c.tookMs ? ms(c.tookMs) : '' }]}
							expand={callBody}
							action={p ? runSkip : undefined}
						/>
					{/each}
				</div>
			{/if}
			{#if turn.error}
				<Str value={turn.error} kind="message" color="error" />
			{:else if turn.stop === 'cancelled'}
				<span class="secondary">Stopped before it finished</span>
			{/if}
		{/if}
	</div>
	{#if !streaming && !editing && (onedit || onretry || ondelete)}
		<footer class="acts">
			{#if turn.text}<CopyButton text={turn.text} label="Copy the text" />{/if}
			{#if aloud}<TurnAction label={speaking ? 'Stop reading aloud' : 'Read aloud'} icon={speaking ? StopIcon : SpeakerHighIcon} onact={() => voice.toggle(turn)} />{/if}
			{#if editable}<TurnAction label="Edit" icon={PencilSimpleIcon} onact={startEdit} />{/if}
			{#if onretry}
				<TurnAction
					label={user ? 'Ask again from here' : 'Write this answer again'}
					icon={ArrowsClockwiseIcon}
					question={beyond > 0 ? (user ? `This removes the ${plural(later, 'message')} after it and writes a new answer` : `This writes the answer again and removes the ${plural(later, 'message')} after it`) : undefined}
					confirm="Ask again"
					onact={onretry}
				/>
			{/if}
			{#if ondelete}<TurnAction label="Delete this message" icon={TrashIcon} question="Delete this message and keep the rest?" confirm="Delete" danger onact={ondelete} />{/if}
			{#if turn.role === 'assistant' && turn.finishedAt && reading}
				<Pop placement="top-start" width="20rem">
					{#snippet content()}{@render statsView()}{/snippet}
					<button type="button" class="reading" aria-label="Timing and tokens, {reading}">{reading}{#if turn.promptTokens}<span class="sep">·</span>{num(turn.promptTokens + turn.completionTokens)} in context{/if}</button>
				</Pop>
			{/if}
		</footer>
	{/if}
</article>

<style>
	.turn {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
	}
	/* A question opens an exchange, so it stands further from the answer before it than from its own */
	.turn.user:not(:first-child) {
		margin-top: var(--nebu-space-3);
	}
	.user {
		align-items: flex-end;
	}
	.meta {
		display: flex;
		gap: var(--nebu-space-2);
		align-items: center;
		min-width: 0;
		height: 28px;
		font-size: var(--nebu-text-secondary);
	}
	.user .meta {
		flex-direction: row-reverse;
	}
	/* Who is speaking is a coin raised from the surface, the person's initial or the model's mark on its face */
	.coin {
		display: inline-grid;
		flex: 0 0 auto;
		place-items: center;
		width: 28px;
		height: 28px;
		font-size: var(--nebu-text-caption);
		font-weight: 500;
		color: var(--nebu-text-muted);
		background: var(--neo-background-color);
		border-radius: 50%;
		box-shadow: var(--neo-box-shadow-convex-2), var(--nebu-machined);
	}
	.turn:not(.user) .coin {
		color: var(--neo-color-primary);
	}
	.state {
		font-size: var(--nebu-text-caption);
		color: var(--neo-color-primary);
	}
	.body {
		position: relative;
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
		border-radius: var(--neo-border-radius-md);
	}
	/* What was typed stays pressed into the surface, like the box it was typed in */
	.user .body {
		width: fit-content;
		max-width: min(100%, 38rem);
		padding: 10px 16px;
		box-shadow: var(--neo-box-shadow-inset-2);
	}
	.user .body.bare {
		display: none;
	}
	.user .body.editing {
		width: 100%;
		padding: var(--nebu-space-2);
	}
	.user :global(.markdown) {
		width: auto;
		font-weight: 400;
		contain: none;
	}
	/* An answer is a slab raised from the surface, a lamp down its edge lit while the words arrive and dim once they end */
	.turn:not(.user) .body {
		min-height: 44px;
		padding: 14px 18px 14px 26px;
		background: var(--neo-background-color);
		box-shadow: var(--neo-box-shadow-raised-2), var(--nebu-machined);
	}
	.lamp {
		position: absolute;
		top: 12px;
		bottom: 12px;
		left: 11px;
		width: 3px;
		background: var(--neo-shadow-color-dark);
		border-radius: 2px;
		box-shadow: 1px 1px 0 var(--neo-shadow-color-light);
	}
	.streaming .lamp {
		background: var(--neo-color-primary);
		box-shadow: 0 0 10px color-mix(in srgb, var(--neo-color-primary) 70%, transparent);
		animation: lamp-live 1.4s ease-in-out infinite;
	}
	.fresh .lamp {
		animation: lamp-settle 900ms var(--nebu-ease) backwards;
	}
	.failed .lamp {
		background: var(--neo-color-error);
		box-shadow: none;
	}
	@keyframes lamp-live {
		50% {
			opacity: 0.45;
		}
	}
	@keyframes lamp-settle {
		from {
			background: var(--neo-color-primary);
			box-shadow: 0 0 10px color-mix(in srgb, var(--neo-color-primary) 70%, transparent);
		}
	}
	/* A turn's words fill its column from the left, tables included, with no gap under the last block */
	.turn :global(.markdown) {
		max-width: none;
		margin: 0;
	}
	.turn :global(.markdown > :last-child) {
		margin-bottom: 0;
	}
	.turn :global(.markdown table) {
		margin: 0;
	}
	.editor {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
	}
	.editor :global(.neo-textarea-group) {
		min-width: 0;
		margin: 0;
	}
	.editfoot {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-2);
		align-items: center;
		justify-content: flex-end;
	}
	.note {
		flex: 1 1 12rem;
		min-width: 0;
	}
	/* The ways to change a message sit under it, quiet until the pointer or the keys are on the message */
	.acts {
		display: flex;
		flex-wrap: wrap;
		gap: 2px;
		align-items: center;
		min-height: 28px;
		color: var(--nebu-text-muted);
		opacity: 0.55;
		transition: opacity var(--nebu-fast) var(--nebu-ease);
	}
	.turn:hover .acts,
	.turn:focus-within .acts,
	.turn:last-child .acts {
		opacity: 1;
	}
	@media (hover: none) {
		.acts {
			opacity: 1;
		}
	}
	.reading {
		display: inline-flex;
		gap: 6px;
		align-items: center;
		min-height: 28px;
		margin-left: var(--nebu-space-2);
		padding: 0 6px;
		font: inherit;
		font-size: var(--nebu-text-caption);
		font-variant-numeric: tabular-nums;
		color: inherit;
		background: none;
		border: 0;
		border-radius: var(--neo-border-radius);
		cursor: pointer;
		appearance: none;
	}
	.reading:hover {
		color: var(--neo-text-color);
	}
	.reading:focus-visible {
		outline: 2px solid var(--neo-color-primary);
		outline-offset: -2px;
	}
	.sep {
		color: var(--nebu-text-faint);
	}
	.thumb {
		max-width: 160px;
		max-height: 160px;
		object-fit: cover;
		border-radius: var(--neo-border-radius);
	}
	.made {
		max-width: min(100%, 256px);
		max-height: 256px;
		border-radius: var(--neo-border-radius);
	}
	.video {
		max-width: min(100%, 512px);
		border-radius: var(--neo-border-radius);
	}
	.clip {
		width: min(100%, 320px);
		height: 40px;
	}
	.lines {
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	.calls {
		--nebu-row-inset: var(--nebu-space-3);

		display: flex;
		flex-direction: column;
		min-width: 0;
		overflow: hidden;
		border: 1px solid var(--nebu-border);
		border-radius: var(--neo-border-radius);
	}
</style>
