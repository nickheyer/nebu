<script lang="ts">
	import { reason, rpc } from '#lib/api/transport.js';
	import { clock, date, enumLabel, ms, num, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { ApiFlavorSchema } from '#lib/proto/nebu/v1/runtime_pb.js';
	import type { Trace } from '#lib/proto/nebu/v1/gateway_pb.js';
	import Facts, { type FactItem } from '#lib/ui/Facts.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Code from '#lib/ui/fields/Code.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { statusClassLamp } from '#lib/ui/status.js';
	import { capped, finished, firstToken, kindWord, started } from '#lib/pages/traffic/traffic.js';

	let { id, onclose }: { id: string; onclose: () => void } = $props();

	let trace = $state<Trace | undefined>(undefined);
	let error = $state('');
	let tab = $state('summary');

	$effect(() => {
		const want = id;
		trace = undefined;
		error = '';
		void rpc.gateway
			.getTrace({ id: want })
			.then((r) => (trace = r.trace))
			.catch((err) => (error = reason(err)));
	});

	// The stream's copy keeps the summary fresh while the request runs
	$effect(() => {
		const fresh = live.traces.find((t) => t.id === id);
		if (fresh && trace && !trace.finishedAt) trace = { ...trace, ...fresh, request: trace.request, upstreamRequest: trace.upstreamRequest, response: trace.response };
	});

	const t = $derived(trace);
	// Started, first byte, first token and finished with the time each took after the one before
	const strip = $derived.by((): FactItem[] => {
		if (!t) return [];
		const s = started(t);
		const fb = date(t.firstByteAt)?.getTime();
		const ft = firstToken(t);
		const end = finished(t);
		const out: FactItem[] = [{ id: 'started', label: 'started', value: clock(t.startedAt) }];
		if (fb) out.push({ id: 'firstbyte', label: 'first byte', value: ms(fb - s), tip: clock(t.firstByteAt) });
		if (ft) out.push({ id: 'firsttoken', label: 'first token', value: ms(ft - (fb ?? s)), tip: clock(t.firstTokenAt) });
		if (end) out.push({ id: 'finished', label: 'finished', value: ms(end - (ft ?? fb ?? s)), tip: clock(t.finishedAt) });
		return out;
	});
</script>

{#if t}
	<PanelHeader title={t.route || kindWord(t.kind)} subtitle={when(t.startedAt)} {onclose}>
		<StatePill label={t.status ? String(t.status) : 'in flight'} lamp={t.status ? statusClassLamp(t.status) : 'warning'} />
	</PanelHeader>
	<PageTabs tabs={[{ id: 'summary', label: 'Summary' }, { id: 'routing', label: 'Routing' }, { id: 'tools', label: 'Tools', count: t.toolCalls.length }, { id: 'bodies', label: 'Bodies' }]} active={tab} onchange={(x) => (tab = x)} />
	<div class="body">
		{#if tab === 'summary'}
			<Stack gap={16}>
				<Facts items={strip} size="section" />
				{#if t.error}<p class="error-text">{t.error}</p>{/if}
				<KeyRows
					rows={[
						{ label: 'request id', value: t.requestId || t.id, mono: true, copy: true },
						{ label: 'route', value: t.route },
						{ label: 'pool', value: t.poolId },
						{ label: 'endpoint', value: t.endpoint, mono: true },
						{ label: 'kind', value: kindWord(t.kind) },
						{ label: 'client api', value: enumLabel(ApiFlavorSchema, t.clientApi) },
						{ label: 'upstream api', value: enumLabel(ApiFlavorSchema, t.upstreamApi) },
						{ label: 'stream', value: t.stream ? 'yes' : 'no' },
						{ label: 'status', value: t.status ? String(t.status) : '' },
						{ label: 'stop', value: t.stop },
						{ label: 'tokens', value: `${num(t.promptTokens)} in, ${num(t.completionTokens)} out` },
						{ label: 'cached', value: t.cachedTokens ? num(t.cachedTokens) : '' },
						{ label: 'queued behind', value: t.queuedBehind ? num(t.queuedBehind) : '' },
						{ label: 'queue wait', value: t.queueWaitMs ? ms(t.queueWaitMs) : '' },
						{ label: 'objective', value: t.objective ? `${t.objective}, ${t.objectiveAttained ? 'attained' : 'missed'}` : '' },
						{ label: 'priority', value: t.objective ? String(t.priority) : '' },
						{ label: 'owner', value: t.owner },
						{ label: 'remote', value: t.remote, mono: true },
						{ label: 'node', value: live.nodes.get(t.nodeId)?.name ?? t.nodeId },
						{ label: 'entry node', value: live.nodes.get(t.entryNode)?.name ?? t.entryNode },
						{ label: 'seats', value: t.seats.map((s) => live.instances.get(s)?.name ?? s).join(', ') },
						{ label: 'relay', value: t.relay },
						{ label: 'draft', value: t.draftOffered ? `${t.draftAccepted} of ${t.draftOffered} accepted` : '' }
					]}
				/>
			</Stack>
		{:else if tab === 'routing'}
			<Stack gap={16}>
				<KeyRows rows={[{ label: 'profile', value: t.profile }, { label: 'score', value: t.score ? t.score.toFixed(3) : '' }]} />
				{#if t.filters.length}
					<Stack gap={0}>
						{#each t.filters as f (f.name)}
							<ListRow primary={f.name} primaryTip={f.dropped.join('\n') || 'nothing dropped'} facts={[{ value: `${num(f.dropped.length)} dropped` }]} />
						{/each}
					</Stack>
				{/if}
				{#if t.candidates.length}
					<Stack gap={0}>
						{#each t.candidates as c (c.endpoint)}
							<ListRow primary={c.endpoint} primaryKind="address" primaryTip={Object.entries(c.byScorer).map(([k, v]) => `${k} ${v.toFixed(3)}`).join('\n')} facts={[{ value: c.score.toFixed(3) }]} />
						{/each}
					</Stack>
				{/if}
				{#if t.fallbacks.length}
					<Stack gap={0}>
						{#each t.fallbacks as f, i (i)}
							<ListRow primary={f.address} primaryKind="address" secondary={f.reason} secondaryKind="message">
								{#snippet badges()}{#if f.requestWritten}<StatePill label="written" lamp="warning" />{/if}{/snippet}
							</ListRow>
						{/each}
					</Stack>
				{/if}
			</Stack>
		{:else if tab === 'tools'}
			{#if t.toolCalls.length === 0}
				<span class="secondary">No tool calls</span>
			{:else}
				<Stack gap={0}>
					{#each t.toolCalls as c (c.id)}
						<ListRow lamp={c.failed ? 'error' : 'success'} primary={c.name} secondary={c.server || 'client'} secondaryWidth="24ch" facts={[{ value: ms(c.tookMs) }]}>
							{#snippet expand()}
								<Code value={c.arguments} language="json" readonly rows={4} label="Arguments" />
								<Code value={c.result} language="json" readonly rows={6} label="Result" />
							{/snippet}
						</ListRow>
					{/each}
				</Stack>
			{/if}
		{:else}
			<Stack gap={16}>
				<Code value={t.request} language="json" readonly rows={8} label="Request" capped={capped(t.request, t.requestBytes)} />
				{#if t.upstreamRequest}<Code value={t.upstreamRequest} language="json" readonly rows={8} label="Upstream request" capped={capped(t.upstreamRequest)} />{/if}
				<Code value={t.response} language="json" readonly rows={8} label="Response" capped={capped(t.response, t.responseBytes)} />
			</Stack>
		{/if}
	</div>
{:else if error}
	<PanelHeader title="Trace" {onclose} />
	<p class="error-text">{error}</p>
{:else}
	<PanelHeader title="Trace" {onclose} />
	<Skeleton rows={4} />
{/if}

<style>
	.body {
		margin-top: 12px;
	}
</style>
