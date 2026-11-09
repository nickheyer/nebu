<script lang="ts">
	import { untrack } from 'svelte';
	import { timestampFromDate } from '@bufbuild/protobuf/wkt';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { isCancel, reason, rpc } from '#lib/api/transport.js';
	import { clock, ms, num, pct, perMinute, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { TraceKind, type Trace } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { prefs } from '#lib/state/prefs.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import Facts, { type FactItem } from '#lib/ui/Facts.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import Pop from '#lib/ui/Pop.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import Toolbar from '#lib/ui/layout/Toolbar.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { statusClassLamp } from '#lib/ui/status.js';
	import { gatewayHeaderTabs } from '#lib/pages/gateway/tabs.js';
	import Timeline from '#lib/pages/traffic/Timeline.svelte';
	import TracePanel from '#lib/pages/traffic/TracePanel.svelte';
	import { buckets, failed, finished, isShed, isWindow, kindWord, marks, percentile, rangeStartDefault, stamp, started, tpot, traceKinds, ttft, windowMs, type Window } from '#lib/pages/traffic/traffic.js';
	import ArrowBendUpRightIcon from 'phosphor-svelte/lib/ArrowBendUpRightIcon';
	import ArrowsClockwiseIcon from 'phosphor-svelte/lib/ArrowsClockwiseIcon';
	import ClockIcon from 'phosphor-svelte/lib/ClockIcon';
	import FunnelIcon from 'phosphor-svelte/lib/FunnelIcon';
	import FunnelXIcon from 'phosphor-svelte/lib/FunnelXIcon';
	import PauseIcon from 'phosphor-svelte/lib/PauseIcon';
	import PlayIcon from 'phosphor-svelte/lib/PlayIcon';
	import PulseIcon from 'phosphor-svelte/lib/PulseIcon';
	import ShieldWarningIcon from 'phosphor-svelte/lib/ShieldWarningIcon';
	import TranslateIcon from 'phosphor-svelte/lib/TranslateIcon';

	let { traceId = '' }: { traceId?: string } = $props();

	const sparkBuckets = 24;
	const initial = untrack(() => page.url.searchParams);
	const urlFrom = Number(initial.get('from') ?? '');
	const urlTo = Number(initial.get('to') ?? '');
	const opened = Date.now();

	let route = $state<string | undefined>(initial.get('route') || undefined);
	let kind = $state<TraceKind | undefined>(undefined);
	let statusClass = $state<number | undefined>(undefined);
	let windowId = $state<Window>(isWindow(initial.get('window')) ? (initial.get('window') as Window) : isWindow(prefs.trafficWindow) ? (prefs.trafficWindow as Window) : '5m');
	let fromAgo = $state(urlFrom > 0 ? Math.max(1, Math.round((opened - urlFrom) / 1000)) : rangeStartDefault);
	let toAgo = $state(urlTo > 0 ? Math.max(0, Math.round((opened - urlTo) / 1000)) : 0);
	let anchor = $state(opened);
	let paused = $state(false);
	let frozen = $state(0);
	let pinned = $state.raw(new Map<string, Trace>());
	let nodeId = $state<string | undefined>(undefined);
	let objective = $state<string | undefined>(undefined);
	let fairness = $state('');
	let view = $state<'list' | 'timeline'>(prefs.trafficTimeline ? 'timeline' : 'list');
	let stats = $state<Trace[]>([]);
	let statsSettled = $state(false);
	let statsError = $state('');
	let toolbarWidth = $state(0);

	// Kind and status, then the route and the range, fold into the filters popover as the card narrows
	const ranged = $derived(windowId === 'range' ? 220 : 0);
	const fold = $derived<'none' | 'some' | 'all'>(viewport.mobile ? 'all' : toolbarWidth === 0 || toolbarWidth >= 1080 + ranged ? 'none' : toolbarWidth >= 740 + ranged ? 'some' : 'all');

	$effect(() => {
		prefs.trafficWindow = windowId;
		prefs.trafficTimeline = view === 'timeline';
	});

	// A range is held as offsets from the moment either was last set
	$effect(() => {
		void fromAgo;
		void toAgo;
		anchor = untrack(() => Date.now());
	});

	const now = $derived(paused ? frozen : clockTick.now);
	const rangeFrom = $derived(anchor - fromAgo * 1000);
	const rangeTo = $derived(anchor - toAgo * 1000);
	const bounds = $derived(windowId === 'range' ? { from: rangeFrom, to: rangeTo } : { from: now - windowMs[windowId], to: 0 });
	// The window edge asked of the daemon, moved once a minute
	const queryFrom = $derived(windowId === 'range' ? rangeFrom : Math.floor(bounds.from / 60_000) * 60_000);
	const queryTo = $derived(windowId === 'range' ? rangeTo : 0);

	type Params = { route: string; kind: TraceKind; statusClass: number; from: number; to: number; nodeId: string; objective: string; fairness: string };
	const params = $derived<Params>({ route: route ?? '', kind: kind ?? TraceKind.UNSPECIFIED, statusClass: statusClass ?? 0, from: queryFrom, to: queryTo, nodeId: nodeId ?? '', objective: objective ?? '', fairness: fairness.trim() });

	function request(p: Params) {
		return {
			route: p.route,
			kind: p.kind,
			statusClass: p.statusClass,
			nodeId: p.nodeId,
			objective: p.objective,
			fairnessId: p.fairness,
			from: p.from ? timestampFromDate(new Date(p.from)) : undefined,
			to: p.to ? timestampFromDate(new Date(p.to)) : undefined
		};
	}

	const list = new ListQuery<Trace, Params>(
		async (p, cursor, limit, signal) => {
			const res = await rpc.gateway.listTraces({ ...request(p), cursor, limit }, { signal });
			return { items: res.traces, next: res.nextCursor };
		},
		[],
		50
	);

	let statsAbort: AbortController | undefined;
	// Every trace of the window for the facts and the timeline, without bodies
	async function loadStats(p: Params) {
		statsAbort?.abort();
		const ac = new AbortController();
		statsAbort = ac;
		try {
			const res = await rpc.gateway.listTraces({ ...request(p), limit: 0 }, { signal: ac.signal });
			if (ac.signal.aborted) return;
			stats = res.traces;
			statsError = '';
		} catch (err) {
			if (isCancel(err) || ac.signal.aborted) return;
			statsError = reason(err);
		} finally {
			if (!ac.signal.aborted) statsSettled = true;
		}
	}

	$effect(() => {
		const p = params;
		void list.load(p);
		void loadStats(p);
	});

	// Refetches both lists at most once a second while traces stream in, a paused page holding still
	$effect(() => {
		let timer: ReturnType<typeof setTimeout> | undefined;
		const stop = live.on(EventKind.TRACE, () => {
			if (paused || timer) return;
			timer = setTimeout(() => {
				timer = undefined;
				if (paused) return;
				list.refresh();
				void loadStats(untrack(() => params));
			}, 1000);
		});
		return () => {
			clearTimeout(timer);
			stop();
			statsAbort?.abort();
		};
	});

	// The query string the filters keep
	const search = $derived.by(() => {
		const q = new URLSearchParams();
		if (route) q.set('route', route);
		q.set('window', windowId);
		if (windowId === 'range') {
			q.set('from', String(Math.round(rangeFrom)));
			q.set('to', String(Math.round(rangeTo)));
		}
		return `?${q.toString()}`;
	});

	$effect(() => {
		const s = search;
		if (s !== location.search) void goto(`${location.pathname}${s}`, { replace: true, shallow: true, reset: false });
	});

	// Stream copies win over fetched ones, a paused page keeping the copies it had
	const fresh = $derived(paused ? pinned : new Map(live.traces.map((t) => [t.id, t])));
	const rows = $derived(list.items.map((t) => fresh.get(t.id) ?? t));
	const windowed = $derived(stats.map((t) => fresh.get(t.id) ?? t).filter((t) => started(t) >= bounds.from && (bounds.to === 0 || started(t) < bounds.to)));
	const done = $derived(windowed.filter((t) => finished(t) !== undefined));
	const end = $derived(bounds.to || now);
	const spanMinutes = $derived(Math.max(1 / 60, (end - bounds.from) / 60_000));
	const ttfts = $derived(windowed.map(ttft).filter((v): v is number => v !== undefined));
	const tpots = $derived(windowed.map(tpot).filter((v): v is number => v !== undefined));
	const waits = $derived(windowed.map((t) => t.queueWaitMs).filter((v) => v > 0));
	const errorRate = $derived(done.length ? (done.filter(failed).length / done.length) * 100 : 0);
	const attainment = $derived.by(() => {
		const byObjective = new Map<string, { met: number; total: number }>();
		for (const t of done) {
			if (!t.objective) continue;
			const b = byObjective.get(t.objective) ?? { met: 0, total: 0 };
			b.total++;
			if (t.objectiveAttained) b.met++;
			byObjective.set(t.objective, b);
		}
		return [...byObjective.entries()].map(([name, b]) => ({ label: `${name} attained`, value: pct((b.met / b.total) * 100) }));
	});

	function median(values: number[]): number {
		return percentile(values, 0.5) ?? 0;
	}

	const facts = $derived.by((): FactItem[] => {
		const split = (value: (l: Trace[]) => number) => buckets(windowed, started, bounds.from, end, sparkBuckets, value);
		return [
			{
				id: 'rate',
				label: 'Requests',
				value: perMinute(windowed.length / spanMinutes),
				spark: split((l) => l.length),
				facts: [{ label: 'in window', value: num(windowed.length) }, { label: 'shed', value: num(windowed.filter(isShed).length) }, ...attainment]
			},
			{
				id: 'errors',
				label: 'Errors',
				value: pct(errorRate, 1),
				lamp: errorRate >= 5 ? 'error' : errorRate > 0 ? 'warning' : undefined,
				spark: split((l) => l.filter(failed).length),
				facts: [
					{ label: '4xx', value: num(done.filter((t) => t.status >= 400 && t.status < 500).length) },
					{ label: '5xx', value: num(done.filter((t) => t.status >= 500).length) },
					{ label: 'p95 errors per bucket', value: num(percentile(split((l) => l.filter(failed).length), 0.95)) }
				]
			},
			{
				id: 'ttft',
				label: 'TTFT',
				value: ttfts.length ? ms(median(ttfts)) : '—',
				spark: split((l) => median(l.map(ttft).filter((v): v is number => v !== undefined))),
				facts: [
					{ label: 'p50', value: ttfts.length ? ms(median(ttfts)) : '—' },
					{ label: 'p95', value: ttfts.length ? ms(percentile(ttfts, 0.95)) : '—' },
					{ label: 'queue wait p95', value: waits.length ? ms(percentile(waits, 0.95)) : '—' }
				]
			},
			{
				id: 'tpot',
				label: 'TPOT',
				value: tpots.length ? ms(median(tpots)) : '—',
				spark: split((l) => median(l.map(tpot).filter((v): v is number => v !== undefined))),
				facts: [
					{ label: 'p50', value: tpots.length ? ms(median(tpots)) : '—' },
					{ label: 'p95', value: tpots.length ? ms(percentile(tpots, 0.95)) : '—' }
				]
			}
		];
	});

	const moreFilters = $derived([nodeId, objective, fairness.trim()].filter(Boolean).length);
	const popFilters = $derived(moreFilters + (fold !== 'none' ? [kind, statusClass].filter((v) => v !== undefined).length : 0) + (fold === 'all' && route ? 1 : 0));
	const activeFilters = $derived([route, kind, statusClass].filter((v) => v !== undefined).length + moreFilters);
	const nodes = $derived([...live.nodes.values()].map((n) => ({ value: n.id, label: n.name || n.id })));
	const objectives = $derived([...live.objectives.values()].map((o) => ({ value: o.name, label: o.name })));
	const kinds = traceKinds.map((k) => ({ value: k, label: kindWord(k) }));
	const statusOptions = [2, 3, 4, 5].map((c) => ({ value: c, label: `${c}xx` }));
	const windowOptions: { value: Window; label: string }[] = [
		{ value: '1m', label: 'Live 1m' },
		{ value: '5m', label: 'Live 5m' },
		{ value: '15m', label: 'Live 15m' },
		{ value: 'range', label: 'Range' }
	];
	const endpoints = $derived([...live.pools.values()].flatMap((p) => (p.status?.endpoints ?? []).map((e) => e.address)));
	const rangeLabel = $derived(`${stamp(rangeFrom, now)} to ${stamp(rangeTo, now)}`);

	function nodeName(id: string): string {
		return live.nodes.get(id)?.name || id;
	}

	async function fetchRoutes(query: string, cursor: string, limit: number, signal: AbortSignal) {
		const res = await rpc.gateway.listRoutes({ query, cursor, limit }, { signal });
		return { items: res.routes.map((r) => ({ value: r.name, label: r.name })), next: res.nextCursor };
	}

	function togglePause() {
		if (!paused) {
			frozen = Date.now();
			pinned = new Map(live.traces.map((t) => [t.id, t]));
		}
		paused = !paused;
	}

	function windowChanged(w: Window | undefined) {
		if (w === 'range') {
			fromAgo = Math.round(windowMs[untrack(() => (isWindow(prefs.trafficWindow) && prefs.trafficWindow !== 'range' ? prefs.trafficWindow : '5m')) as Exclude<Window, 'range'>] / 1000);
			toAgo = 0;
		}
	}

	function clearFilters() {
		route = undefined;
		kind = undefined;
		statusClass = undefined;
		nodeId = undefined;
		objective = undefined;
		fairness = '';
	}

	function durationOf(t: Trace): string {
		if (isShed(t)) return 'shed';
		const f = finished(t);
		return f === undefined ? 'in flight' : ms(f - started(t));
	}

	function durationTip(t: Trace): string {
		const first = ttft(t);
		const lines = [`TTFT ${first === undefined ? '—' : ms(first)}`, `${num(t.promptTokens)} prompt, ${num(t.completionTokens)} completion`, `${num(t.cachedTokens)} cached`];
		return isShed(t) && t.error ? [t.error, ...lines].join('\n') : lines.join('\n');
	}

	function hrefOf(id: string): string {
		return `/traffic/${encodeURIComponent(id)}${search}`;
	}

	function close() {
		void goto(`/traffic${search}`);
	}
</script>

{#snippet rangeFields()}
	<Duration bind:value={fromAgo} base="s" label="Start ago" info="How far back the window starts" message={fromAgo <= toAgo ? 'Start after end' : undefined} />
	<Duration bind:value={toAgo} base="s" label="End ago" info="How far back the window ends, now when empty" emptyZero off={{ label: 'Now' }} />
{/snippet}

{#snippet popover()}
	{#if fold === 'all'}
		<Picker bind:value={route} fetch={fetchRoutes} label="Route" compact clearable current={route} />
	{/if}
	{#if fold !== 'none'}
		<Select bind:value={kind} options={kinds} clearable label="Kind" compact />
		<Select bind:value={statusClass} options={statusOptions} clearable label="Status" compact />
	{/if}
	{#if fold === 'all' && windowId === 'range'}{@render rangeFields()}{/if}
	<Select bind:value={nodeId} options={nodes} clearable label="Node" compact search={nodes.length > 8} />
	<Select bind:value={objective} options={objectives} clearable label="Objective" compact search={objectives.length > 8} />
	<Text bind:value={fairness} label="Fairness id" compact max={64} mono />
{/snippet}

{#snippet toolbar()}
	<div class="bar" bind:clientWidth={toolbarWidth}>
		<Toolbar>
			{#if fold !== 'all'}
				<span class="route"><Picker bind:value={route} fetch={fetchRoutes} label="Route" compact clearable current={route} /></span>
			{/if}
			{#if fold === 'none'}
				<span class="kind"><Select bind:value={kind} options={kinds} label="Kind" compact clearable /></span>
				<span class="status"><Select bind:value={statusClass} options={statusOptions} label="Status" compact clearable /></span>
			{/if}
			<span class="window"><Select bind:value={windowId} options={windowOptions} onchange={windowChanged} /></span>
			{#if windowId === 'range' && fold !== 'all'}
				<Pop placement="bottom-start" width="22rem">
					{#snippet content()}{@render rangeFields()}{/snippet}
					<NeoButton elevation={2}>{#snippet icon()}<ClockIcon size={18} />{/snippet}{rangeLabel}</NeoButton>
				</Pop>
			{/if}
			<NeoButton text aria-label={paused ? 'Resume' : 'Pause'} title={paused ? 'Resume' : 'Pause'} onclick={togglePause}>
				{#snippet icon()}{#if paused}<PlayIcon size={18} />{:else}<PauseIcon size={18} />{/if}{/snippet}
			</NeoButton>
			<Pop placement="bottom-start" width="20rem">
				{#snippet content()}{@render popover()}{/snippet}
				<NeoButton elevation={2}>{#snippet icon()}<FunnelIcon size={18} />{/snippet}Filters{#if popFilters}<span class="count">{popFilters}</span>{/if}</NeoButton>
			</Pop>
			{#if !viewport.mobile}
				<span class="push"><Segmented bind:value={view} options={[{ value: 'list', label: 'List' }, { value: 'timeline', label: 'Timeline' }]} /></span>
			{/if}
		</Toolbar>
	</div>
{/snippet}

{#snippet head()}
	<Card>
		{#if !statsSettled}
			<Skeleton rows={1} kind="strip" />
		{:else if statsError}
			<Empty text="Traffic facts failed to load" hint={statsError} action="Retry" onclick={() => loadStats(params)} />
		{:else}
			<Facts items={facts} />
		{/if}
	</Card>
{/snippet}

{#snippet emptyList()}
	{#if activeFilters}
		<Empty icon={FunnelXIcon} text="No requests match" action="Clear filters" onclick={clearFilters} />
	{:else if windowId === 'range'}
		<Empty icon={PulseIcon} text="No requests in the range" action="Live 5m" onclick={() => (windowId = '5m')} />
	{:else if live.ready && live.routes.size === 0}
		<Empty icon={PulseIcon} text="No requests" action="Launch" href="/serve/launch" primary />
	{:else}
		<Empty icon={PulseIcon} text="No requests" action="Chat" href="/chat" primary />
	{/if}
{/snippet}

{#snippet listCard()}
	<Card flush>
		{#snippet tools()}{@render toolbar()}{/snippet}
		{#if view === 'timeline' && !viewport.mobile && statsSettled && !statsError && windowed.length}
			<Timeline traces={windowed} {endpoints} from={bounds.from} to={end} {now} href={hrefOf} {nodeName} />
		{/if}
		{#if !list.settled}
			<Skeleton rows={6} primary="9ch" secondary="8ch" facts={2} action={false} menu={false} />
		{:else if list.error}
			<Empty text="Requests failed to load" hint={list.error} action="Retry" onclick={() => list.refresh()} />
		{:else if rows.length === 0}
			{@render emptyList()}
		{:else}
			<List head={{ primary: 'Time', secondary: 'Kind', instrument: 'Route', facts: ['Status', 'Duration'] }}>
				{#each rows as t (t.id)}
					<ListRow
						lamp={t.status ? statusClassLamp(t.status) : 'warning'}
						shape={finished(t) === undefined ? 'half' : 'filled'}
						primary={clock(t.startedAt)}
						primaryKind="text"
						primaryWidth="9ch"
						primaryTip={when(t.startedAt)}
						secondary={kindWord(t.kind)}
						secondaryWidth="8ch"
						facts={[
							{ value: t.status ? String(t.status) : 'open', tip: t.error || undefined },
							{ value: durationOf(t), tip: durationTip(t) }
						]}
						selected={t.id === traceId}
						onclick={() => goto(hrefOf(t.id))}
					>
						{#snippet instrument()}
							<span class="routecell">
								<Str value={t.route || 'no route'} color={t.route ? undefined : 'muted'} width="100%" />
								{#each marks(t, nodeName) as m (m.id)}
									<Tip text={m.tip} tag="span">
										<span class="mark muted" aria-label={m.tip} role="img">
											{#if m.id === 'forwarded'}<ArrowBendUpRightIcon size={16} />{:else if m.id === 'translated'}<TranslateIcon size={16} />{:else if m.id === 'failedOpen'}<ShieldWarningIcon size={16} />{:else}<ArrowsClockwiseIcon size={16} /><span class="caption">{m.count}</span>{/if}
										</span>
									</Tip>
								{/each}
							</span>
						{/snippet}
					</ListRow>
				{/each}
			</List>
			<LoadMore next={list.next} loading={list.loading} onmore={() => list.more()} />
		{/if}
	</Card>
{/snippet}

<PageHeader tabs={gatewayHeaderTabs} active="traffic" info="Every request the gateway handled in the window, with its latency and the path it took" />

<Sheet open={traceId !== ''} onclose={close} {head} list={listCard}>
	{#if traceId}
		{#key traceId}
			<TracePanel id={traceId} onclose={close} />
		{/key}
	{/if}
</Sheet>

<style>
	.bar {
		display: flex;
		flex: 1 1 auto;
		min-width: 0;
	}
	.route {
		display: flex;
		flex: 0 1 14rem;
		min-width: 10rem;
	}
	.kind {
		display: flex;
		flex: 0 0 10.5rem;
	}
	.status {
		display: flex;
		flex: 0 0 9.5rem;
	}
	.window {
		display: flex;
		flex: 0 0 10rem;
	}
	.route > :global(*),
	.kind > :global(*),
	.status > :global(*),
	.window > :global(*) {
		flex: 1 1 auto;
		min-width: 0;
	}
	.count {
		margin-left: var(--nebu-space-2);
		font-size: var(--nebu-text-caption);
		font-variant-numeric: tabular-nums;
		color: var(--nebu-text-muted);
	}
	.routecell {
		display: flex;
		flex: 1 1 auto;
		gap: var(--nebu-space-2);
		align-items: center;
		min-width: 0;
	}
	.mark {
		display: inline-flex;
		flex: 0 0 auto;
		gap: 2px;
		align-items: center;
	}
</style>
