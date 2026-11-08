<script lang="ts">
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
	import StatePill from '#lib/ui/StatePill.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Textarea from '#lib/ui/fields/Textarea.svelte';
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
		onresult
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
		onresult?: (c: PendingCall, text: string) => void;
	} = $props();

	let results = $state<Record<string, string>>({});

	const at = (ts: ChatTurn['startedAt']) => date(ts)?.getTime() ?? 0;
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

<div class="turn">
	<Row gap={8} wrap={false}>
		<Str value={name} weight={500} />
		{#if turn.role === 'assistant' && turn.finishedAt}
			<Pop placement="bottom-start" width="20rem">
				{#snippet content()}{@render statsView()}{/snippet}
				<NeoButton text aria-label="Timing and tokens">{#snippet icon()}<GaugeIcon size={16} />{/snippet}</NeoButton>
			</Pop>
		{/if}
	</Row>
	{#if turn.images.length}
		<Row gap={8}>
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
	{#if turn.text}
		<Markdown source={turn.text} />
	{:else if streaming && turn.toolCalls.length === 0}
		<NeoSkeletonText lines={2} width="40ch" />
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
				{#snippet resultBox()}
					{#if p}
						<div class="answer">
							<Stack gap={8}>
								<Textarea bind:value={() => results[c.id] ?? '', (v) => (results[c.id] = v)} label="Result" rows={2} mono />
								<Row gap={8} justify="flex-end">
									<NeoButton text onclick={() => onskip?.(p)}>Skip</NeoButton>
									<NeoButton elevation={2} onclick={() => onresult?.(p, results[c.id] ?? '')}>Send</NeoButton>
								</Row>
							</Stack>
						</div>
					{/if}
				{/snippet}
				<ListRow
					lamp={state.lamp}
					shape={state.shape}
					primary={c.name}
					primaryTip={[c.name, c.server ? `server ${c.server}` : 'client tool', state.word].join('\n')}
					facts={[{ value: c.tookMs ? ms(c.tookMs) : '' }]}
					expand={callBody}
					action={p?.server ? runSkip : undefined}
					below={p && !p.server ? resultBox : undefined}
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
			<StatePill label="stopped" />
			{#if onretry}<NeoButton text onclick={onretry}>Retry</NeoButton>{/if}
		</Row>
	{/if}
</div>

<style>
	.turn {
		display: flex;
		flex-direction: column;
		gap: 8px;
		min-width: 0;
		padding: 12px 0;
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
		display: flex;
		flex-direction: column;
		min-width: 0;
		margin: 0 calc(-1 * var(--nebu-row-inset, 0px));
	}
	.answer {
		flex: 1 1 auto;
		min-width: 0;
		padding: 4px 0 8px 20px;
	}
</style>
