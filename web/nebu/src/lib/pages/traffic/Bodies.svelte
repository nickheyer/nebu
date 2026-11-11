<script lang="ts">
	import { bytes, num } from '#lib/format.js';
	import type { Trace, TraceConversation, TraceTurn } from '#lib/proto/nebu/v1/gateway_pb.js';
	import Collapse from '#lib/ui/Collapse.svelte';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import Body from '#lib/ui/Body.svelte';
	import Lines from '#lib/ui/Lines.svelte';
	import StateWord from '#lib/ui/StateWord.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { capped } from '#lib/pages/traffic/traffic.js';

	// Turns shown under the earlier ones, which fold behind their count
	const tail = 2;
	// Characters of a system prompt shown without folding it
	const systemShort = 200;

	// A trace's request as its turns, tool calls and their results, then the answer, or every body as a JSON tree
	let { trace, conversation }: { trace: Trace; conversation?: TraceConversation } = $props();

	let view = $state<'turns' | 'raw'>('turns');
	const mode = $derived(conversation ? view : 'raw');

	const turns = $derived(conversation?.turns ?? []);
	const called = $derived(new Set(turns.flatMap((t) => t.calls.map((c) => c.id))));
	// The result each call got back, by the id its tool turn answers
	const results = $derived(new Map(turns.filter((t) => t.role === 'tool' && t.toolCallId !== '').map((t) => [t.toolCallId, t])));
	// Tool turns answering a call sit under that call, the rest stand as turns of their own
	const blocks = $derived(turns.filter((t) => !(t.role === 'tool' && called.has(t.toolCallId))));
	const system = $derived(blocks[0]?.role === 'system' ? blocks[0] : undefined);
	const rest = $derived(system ? blocks.slice(1) : blocks);
	const earlier = $derived(rest.length > tail ? rest.slice(0, -tail) : []);
	const latest = $derived(rest.length > tail ? rest.slice(-tail) : rest);

	function textOf(t: TraceTurn): string {
		return t.parts
			.filter((p) => p.type === 'text')
			.map((p) => p.text)
			.join('');
	}
</script>

{#snippet call(name: string, args: string, result: string | undefined)}
	<Collapse label={name} indent={false}>
		<Stack gap={4}>
			<span class="label">Arguments</span>
			<Body text={args} />
			{#if result !== undefined}
				<span class="label">Result</span>
				<Body text={result} />
			{/if}
		</Stack>
	</Collapse>
{/snippet}

{#snippet turn(t: TraceTurn)}
	<div class="turn">
		<div class="head">
			<span class="role">{t.role}</span>
			{#if t.role === 'tool' && t.toolCallId}<span class="muted mono">{t.toolCallId}</span>{/if}
			<span class="fill"></span>
			{#if textOf(t)}<CopyButton text={textOf(t)} />{/if}
		</div>
		{#each t.parts as p, i (i)}
			{#if p.type === 'thinking'}
				<Collapse label="Reasoning" indent={false}><Lines text={p.text} /></Collapse>
			{:else if p.type === 'image'}
				<span class="muted mono">{p.url || `${p.mediaType} ${bytes(p.size)}`}</span>
			{:else if p.type === 'tokens'}
				<span class="muted">{num(p.size)} token ids</span>
			{:else if p.text}
				<Lines text={p.text} />
			{/if}
		{/each}
		{#each t.calls as c (c.id)}
			{@const answered = results.get(c.id)}
			{@render call(c.name, c.arguments, answered ? textOf(answered) : undefined)}
		{/each}
	</div>
{/snippet}

{#snippet section(label: string, text: string, cut: boolean)}
	<div class="turn">
		<div class="head">
			<span class="role">{label}</span>
			<span class="fill"></span>
			{#if cut}<StateWord label="capped" lamp="warning" />{/if}
			<CopyButton {text} />
		</div>
		<Body {text} />
	</div>
{/snippet}

<Stack gap={16}>
	{#if conversation}
		<div class="head">
			<Segmented bind:value={view} options={[{ value: 'turns', label: 'Conversation' }, { value: 'raw', label: 'Raw' }]} />
			<span class="fill"></span>
			{#if mode === 'turns' && capped(trace.request, trace.requestBytes)}<StateWord label="capped" lamp="warning" />{/if}
		</div>
	{/if}
	{#if mode === 'turns' && conversation}
		{#if conversation.tools.length}
			<Collapse label="Tools {num(conversation.tools.length)}" indent={false}>
				{#each conversation.tools as tool (tool.name)}
					<Collapse label={tool.name} indent={false}>
						<Stack gap={4}>
							{#if tool.description}<Lines text={tool.description} maxHeight="12rem" />{/if}
							{#if tool.schema}<Body text={tool.schema} />{:else if tool.type}<span class="muted mono">{tool.type}</span>{/if}
						</Stack>
					</Collapse>
				{/each}
			</Collapse>
		{/if}
		{#if system}
			{#if textOf(system).length <= systemShort}
				{@render turn(system)}
			{:else}
				<Collapse label="System" indent={false}>{@render turn(system)}</Collapse>
			{/if}
		{/if}
		{#if earlier.length}
			<Collapse label="{num(earlier.length)} earlier turns" indent={false}>
				<Stack gap={16}>
					{#each earlier as t, i (i)}{@render turn(t)}{/each}
				</Stack>
			</Collapse>
		{/if}
		{#each latest as t, i (i)}{@render turn(t)}{/each}
		{#if trace.finishedAt}
			<div class="turn">
				<div class="head">
					<span class="role">{trace.error ? 'error' : 'assistant'}</span>
					<span class="fill"></span>
					{#if capped(trace.response, trace.responseBytes)}<StateWord label="capped" lamp="warning" />{/if}
					{#if trace.response}<CopyButton text={trace.response} />{/if}
				</div>
				{#if trace.error}<p class="error-text failure">{trace.error}</p>{/if}
				{#if trace.reasoning}
					<Collapse label="Reasoning" indent={false}><Lines text={trace.reasoning} /></Collapse>
				{/if}
				{#if trace.response}<Body text={trace.response} />{/if}
				{#each trace.toolCalls as c (c.id)}
					{@render call(c.name, c.arguments, c.result || undefined)}
				{/each}
			</div>
		{/if}
	{:else}
		{@render section('Request', trace.request, capped(trace.request, trace.requestBytes))}
		{#if trace.upstreamRequest}{@render section('Request sent to the runtime', trace.upstreamRequest, capped(trace.upstreamRequest))}{/if}
		{#if trace.reasoning}{@render section('Reasoning', trace.reasoning, false)}{/if}
		{#if trace.response}{@render section('Response', trace.response, capped(trace.response, trace.responseBytes))}{/if}
	{/if}
</Stack>

<style>
	.turn {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-1);
		min-width: 0;
	}
	.head {
		display: flex;
		gap: var(--nebu-space-2);
		align-items: center;
		min-height: 28px;
	}
	.role {
		font-size: var(--nebu-text-secondary);
		font-weight: 500;
	}
	.fill {
		flex: 1 1 auto;
	}
	.failure {
		overflow-wrap: anywhere;
		white-space: pre-wrap;
	}
</style>
