<script lang="ts">
	import { untrack, type Snippet } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoSkeletonText } from '#lib/neo/skeletons/index.js';
	import { date, ms, plural, shortId, tokensPerSecond } from '#lib/format.js';
	import type { ChatTurn } from '#lib/proto/nebu/v1/chat_pb.js';
	import type { TraceToolCall } from '#lib/proto/nebu/v1/gateway_pb.js';
	import type { ChatSession, PendingCall } from '#lib/pages/chat/chat.svelte.js';
	import KeyRows, { type KeyRow } from '#lib/ui/KeyRows.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import Markdown from '#lib/ui/Markdown.svelte';
	import Pop from '#lib/ui/Pop.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import type { DotShape, Lamp } from '#lib/ui/status.js';
	import GaugeIcon from 'phosphor-svelte/lib/GaugeIcon';

	let {
		turn,
		session,
		name,
		pending = [],
		streaming = false,
		traceLinks = false,
		onretry,
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
		onretry?: () => void;
		onrun?: (c: PendingCall) => void;
		onskip?: (c: PendingCall) => void;
		children?: Snippet;
	} = $props();

	const at = (ts: ChatTurn['startedAt']) => date(ts)?.getTime() ?? 0;
	const user = $derived(turn.role === 'user');
	// An answer that ended within the last moments dims its rail from lit instead of appearing dim
	const fresh = untrack(() => turn.role === 'assistant' && !streaming && Date.now() - at(turn.finishedAt) < 2000);
	const ttft = $derived(turn.firstTokenAt && turn.startedAt ? at(turn.firstTokenAt) - at(turn.startedAt) : undefined);
	const tps = $derived.by(() => {
		if (!turn.firstTokenAt || !turn.finishedAt || turn.completionTokens < 2) return undefined;
		const span = at(turn.finishedAt) - at(turn.firstTokenAt);
		return span > 0 ? ((turn.completionTokens - 1) / span) * 1000 : undefined;
	});
	const stats = $derived<KeyRow[]>([
		{ label: 'TTFT', value: ttft === undefined ? '' : ms(ttft) },
		{ label: 'Speed', value: tps === undefined ? '' : tokensPerSecond(tps) },
		{ label: 'Prompt', value: turn.promptTokens ? plural(turn.promptTokens, 'token') : '' },
		{ label: 'Completion', value: turn.completionTokens ? plural(turn.completionTokens, 'token') : '' },
		{ label: 'Stop', value: turn.stop },
		traceLinks
			? { label: 'Trace', value: shortId(turn.traceId), tip: turn.traceId, href: `/traffic/${turn.traceId}`, mono: true }
			: { label: 'Trace', value: turn.traceId, mono: true, copy: true }
	]);

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

<div class="turn" class:user class:streaming class:fresh class:failed={turn.error !== ''}>
	<div class="meta">
		<Str value={name} color="muted" />
		{#if turn.role === 'assistant' && turn.finishedAt}
			<Pop placement="bottom-start" width="20rem">
				{#snippet content()}{@render statsView()}{/snippet}
				<NeoButton text aria-label="Timing and tokens">{#snippet icon()}<GaugeIcon size={16} />{/snippet}</NeoButton>
			</Pop>
		{/if}
	</div>
	{#if turn.images.length}
		<Row gap={8} justify={user ? 'flex-end' : 'flex-start'}>
			{#each turn.images as f, i (i)}
				{@const url = session.fileUrl(f)}
				{#if url}
					<img class="thumb" src={url} alt={f.name} />
				{:else}
					<NeoSkeletonText lines={1} width="160px" height="160px" />
				{/if}
			{/each}
		</Row>
	{/if}
	<div class="body">
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
						<NeoSkeletonText lines={1} width="256px" height="256px" />
					{:else if f.mediaType.startsWith('video/')}
						<video class="video" src={url} controls><track kind="captions" /></video>
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
						primaryTip={[c.name, c.server, state.word].filter(Boolean).join('\n')}
						facts={[{ value: c.tookMs ? ms(c.tookMs) : '' }]}
						expand={callBody}
						action={p ? runSkip : undefined}
					/>
				{/each}
			</div>
		{/if}
		{#if turn.error}
			<Row gap={8} wrap={false}>
				<Str value={turn.error} kind="message" color="error" />
				{#if onretry}<NeoButton text onclick={onretry}>Retry</NeoButton>{/if}
			</Row>
		{:else if turn.stop === 'cancelled'}
			<Row gap={8} wrap={false}>
				<span class="secondary">Stopped</span>
				{#if onretry}<NeoButton text onclick={onretry}>Retry</NeoButton>{/if}
			</Row>
		{/if}
	</div>
</div>

<style>
	.turn {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-1);
		min-width: 0;
	}
	/* A question opens an exchange, so it stands further from the answer before it than from its own */
	.turn.user:not(:first-child) {
		margin-top: var(--nebu-space-3);
	}
	.meta {
		display: flex;
		gap: var(--nebu-space-1);
		align-items: center;
		min-width: 0;
		height: 24px;
		font-size: var(--nebu-text-caption);
	}
	.user {
		align-items: flex-end;
	}
	.body {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
	}
	/* What was typed stays in a well like the box it was typed in */
	.user .body {
		width: fit-content;
		max-width: min(100%, 34rem);
		padding: 10px 14px;
		border-radius: var(--neo-border-radius-md);
		box-shadow: var(--neo-box-shadow-inset-2);
	}
	.user :global(.markdown) {
		width: auto;
		font-weight: 400;
		contain: none;
	}
	/* An answer hangs off a rail, a lit cursor that stretches while the words arrive and dims into a groove once they end */
	.turn:not(.user) .body {
		position: relative;
		min-height: 24px;
		padding-left: var(--nebu-space-4);
	}
	.turn:not(.user) .body::before {
		position: absolute;
		top: 3px;
		bottom: 3px;
		left: 0;
		width: 2px;
		content: '';
		background: var(--neo-shadow-color-dark);
		border-radius: 1px;
		box-shadow: 1px 1px 0 var(--neo-shadow-color-light);
	}
	.turn.streaming:not(.user) .body::before {
		background: var(--neo-color-primary);
		box-shadow: 0 0 8px color-mix(in srgb, var(--neo-color-primary) 60%, transparent);
		animation: rail-live 1.4s ease-in-out infinite;
	}
	.turn.fresh:not(.user) .body::before {
		animation: rail-settle 900ms var(--nebu-ease) backwards;
	}
	.turn.failed:not(.user) .body::before {
		background: var(--neo-color-error);
		box-shadow: none;
	}
	@keyframes rail-live {
		50% {
			opacity: 0.45;
		}
	}
	@keyframes rail-settle {
		from {
			background: var(--neo-color-primary);
			box-shadow: 0 0 8px color-mix(in srgb, var(--neo-color-primary) 60%, transparent);
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
