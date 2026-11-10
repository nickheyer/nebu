<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { timestampFromDate } from '@bufbuild/protobuf/wkt';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { isCancel, reason, rpc } from '#lib/api/transport.js';
	import { bytes, bytesIn, byteUnit, enumLabel, factLabel, perMinute, thin, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import type { GatewayStatus, Trace } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { DeviceKind, PoolKind, ProbeStatus, ProbeStatusSchema, type Device, type ProbeResult, type ProcessUse, type Storage } from '#lib/proto/nebu/v1/host_pb.js';
	import type { Instance } from '#lib/proto/nebu/v1/instance_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import Bar, { type Segment } from '#lib/ui/Bar.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import Facts from '#lib/ui/Facts.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow, { type Fact } from '#lib/ui/ListRow.svelte';
	import LogView from '#lib/ui/LogView.svelte';
	import Mark from '#lib/ui/Mark.svelte';
	import MemoryLegend from '#lib/ui/MemoryLegend.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Sparkline from '#lib/ui/Sparkline.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import Columns from '#lib/ui/layout/Columns.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { poolBars, poolTitle, type PoolBar } from '#lib/ui/memory.js';
	import { instanceLamp, instanceShape } from '#lib/ui/status.js';
	import { hostTabs } from '#lib/pages/host/tabs.js';
	import { modelPhrase, terminal } from '#lib/pages/serve/serve.js';
	import { buckets, sparkBuckets, started, windowFacts } from '#lib/pages/traffic/traffic.js';
	import ArrowsClockwiseIcon from 'phosphor-svelte/lib/ArrowsClockwiseIcon';
	import PlayIcon from 'phosphor-svelte/lib/PlayIcon';

	let { tab = 'overview' }: { tab?: string } = $props();

	// Minutes of traffic the overview reads
	const trafficMinutes = 15;

	const profile = $derived(live.host);
	let refreshing = $state(false);
	let gateway = $state<GatewayStatus | undefined>(undefined);
	let gatewayError = $state('');
	let traces = $state<Trace[]>([]);
	let tracesSettled = $state(false);
	let tracesError = $state('');
	const stop = new AbortController();
	onDestroy(() => stop.abort());

	// Every trace of the window, without bodies
	async function loadTraces() {
		try {
			const from = timestampFromDate(new Date(Date.now() - trafficMinutes * 60_000));
			traces = (await rpc.gateway.listTraces({ from, limit: 0 }, { signal: stop.signal })).traces;
			tracesError = '';
		} catch (err) {
			if (isCancel(err)) return;
			tracesError = reason(err);
		} finally {
			tracesSettled = true;
		}
	}

	// The overview reads the daemon's listeners once and the traffic as it arrives, the log tab neither
	onMount(() => {
		if (tab !== 'overview') return;
		void rpc.gateway
			.getGatewayStatus({}, { signal: stop.signal })
			.then((r) => (gateway = r.status))
			.catch((err) => {
				if (!isCancel(err)) gatewayError = reason(err);
			});
		void loadTraces();
		let timer: ReturnType<typeof setTimeout> | undefined;
		const off = live.on(EventKind.TRACE, () => {
			timer ??= setTimeout(() => {
				timer = undefined;
				void loadTraces();
			}, 1000);
		});
		return () => {
			clearTimeout(timer);
			off();
		};
	});

	const windowFrom = $derived(clockTick.now - trafficMinutes * 60_000);
	const windowed = $derived(traces.filter((t) => started(t) >= windowFrom));
	const trafficFacts = $derived(windowFacts(windowed, windowFrom, clockTick.now));
	const serving = $derived([...live.instances.values()].filter((i) => !terminal(i.state)).sort((a, b) => a.name.localeCompare(b.name)));

	// Failed checks first, then the ones that passed, then the skipped
	const order = [ProbeStatus.FAILED, ProbeStatus.OK, ProbeStatus.SKIPPED];
	const probes = $derived([...(profile?.probes ?? [])].sort((a, b) => order.indexOf(a.status) - order.indexOf(b.status)));
	// Host facts beyond the os and arch the system row already reads
	const facts = $derived(
		Object.entries(profile?.facts ?? {})
			.filter(([k]) => k !== 'os' && k !== 'arch')
			.sort(([a], [b]) => a.localeCompare(b))
	);

	const bars = $derived(poolBars(profile, live.instances.values(), live.slots.values()));
	const kinds = $derived(bars.flatMap((b) => b.segments.filter((s) => s.bytes > 0).map((s) => s.kind)));

	// Probes the host again and checks its dependencies as one task
	async function refresh() {
		refreshing = true;
		try {
			followTask((await rpc.host.doctor({})).task);
		} catch (err) {
			toast.error('Probe failed', reason(err));
		} finally {
			refreshing = false;
		}
	}

	function tracesOf(inst: Instance): Trace[] {
		return windowed.filter((t) => t.instanceId === inst.id || t.seats.includes(inst.id));
	}

	function hostLogs(tail: number, follow: boolean, signal: AbortSignal) {
		const stream = rpc.host.logs({ tail, follow }, { signal });
		return (async function* () {
			for await (const res of stream) yield res.lines;
		})();
	}

	function processesOf(bar: PoolBar) {
		const ids = new Set(bar.devices.map((d) => d.id));
		return (profile?.processes ?? []).filter((p) => ids.has(p.deviceId)).sort((a, b) => Number(b.bytes - a.bytes));
	}

	// Memory is accounted per process on accelerators only, never on the host's RAM
	function accelerated(bar: PoolBar): boolean {
		return bar.devices.some((d) => d.kind !== DeviceKind.CPU);
	}

	function processName(p: ProcessUse): string {
		return p.instanceName || p.command || String(p.pid);
	}

	// The pid stands beside a named process and is left out when it is the name
	function processFacts(p: ProcessUse): Fact[] {
		const named = p.instanceName !== '' || p.command !== '';
		return [...(named ? [{ value: String(p.pid), tip: 'pid', mono: true }] : []), { value: bytes(p.bytes) }];
	}

	function deviceRows(d: Device) {
		return [
			{ label: 'ID', value: d.id, mono: true, copy: true, tip: d.id },
			{ label: 'vendor', value: d.vendor },
			{ label: 'name', value: d.name, tip: d.name },
			{ label: 'memory', value: d.memoryTotalBytes ? `${bytes(d.memoryTotalBytes - d.memoryFreeBytes)} of ${bytes(d.memoryTotalBytes)}` : poolMemory(d.poolId) },
			...Object.entries(d.facts)
				.sort(([a], [b]) => a.localeCompare(b))
				.map(([k, v]) => ({ label: factLabel(k), value: v, mono: true, tip: v }))
		];
	}

	// The pool a device without memory of its own draws on
	function poolMemory(poolId: string): string {
		const pool = profile?.pools.find((p) => p.id === poolId);
		return pool ? `${bytes(pool.totalBytes)} ${poolTitle(pool.kind as PoolKind)}` : '';
	}

	// Used over total in the unit of the total, such as 3.1 / 12.0 GB
	function usedOverTotal(used: number | bigint, total: number | bigint): string {
		return `${bytesIn(used, total)}${thin}/${thin}${bytesIn(total, total)}${thin}${byteUnit(total)}`;
	}

	function storageUsed(s: Storage): number {
		return Number(s.totalBytes - s.freeBytes);
	}

	// The store's own bytes in the accent, the rest of the mount's use in grey
	function storageSegments(s: Storage): Segment[] {
		const total = storageUsed(s);
		const store = live.store;
		const own = store && s.uses.includes('store') ? Math.min(total, Number(store.blobBytes + store.partialBytes + store.cacheBytes)) : 0;
		const segments: Segment[] = own > 0 ? [{ kind: 'used', label: 'store', bytes: own }, { kind: 'other', label: 'other', bytes: total - own }] : [{ kind: 'other', label: 'other', bytes: total }];
		return [...segments, { kind: 'free', label: 'free', bytes: Number(s.freeBytes) }];
	}

	function probeState(p: ProbeResult): 'ok' | 'failed' | 'skipped' {
		return p.status === ProbeStatus.OK ? 'ok' : p.status === ProbeStatus.FAILED ? 'failed' : 'skipped';
	}
</script>

{#snippet deviceRow(bar: PoolBar)}
	<ListRow primary={bar.name || bar.label} primaryWidth="30ch" primaryTip={bar.devices.map((d) => d.name).join('\n') || undefined}>
		{#snippet leading()}<span class="devlabel">{bar.label}</span>{/snippet}
		{#snippet instrument()}
			<div class="gauge">
				<span class="track"><Bar segments={bar.segments} total={bar.total} format={(n) => bytes(n)} /></span>
				<Tip text="{bytes(bar.free)} free" tag="span"><span class="reading">{usedOverTotal(bar.used, bar.total)}</span></Tip>
			</div>
		{/snippet}
		{#snippet expand()}
			<Stack gap={12}>
				{#each bar.devices as d (d.id)}
					<KeyRows dense rows={deviceRows(d)} />
				{/each}
				{#if accelerated(bar)}
					{@const procs = processesOf(bar)}
					<Stack gap={0}>
						<span class="label">Processes</span>
						{#each procs as p (`${p.deviceId}:${p.pid}`)}
							<ListRow primary={processName(p)} primaryKind={p.instanceName ? 'name' : 'text'} href={p.instanceId ? `/serve/${encodeURIComponent(p.instanceId)}` : undefined} facts={processFacts(p)} />
						{:else}
							<Empty text="No processes" row />
						{/each}
					</Stack>
				{/if}
			</Stack>
		{/snippet}
	</ListRow>
{/snippet}

{#snippet devicesCard()}
	<Card title="Devices" flush>
		{#if !profile}
			<Skeleton rows={2} kind="bars" />
		{:else if bars.length === 0}
			<Empty text="No devices probed" />
		{:else}
			<List head={{ primary: 'Device', instrument: 'Memory' }}>
				{#each bars as bar (bar.id)}{@render deviceRow(bar)}{/each}
			</List>
			<div class="legend"><MemoryLegend {kinds} /></div>
		{/if}
	</Card>
{/snippet}

{#snippet storageCard()}
	<Card title="Storage" flush>
		{#if !profile}
			<Skeleton rows={2} dot={false} primary="40ch" secondary="8ch" facts={1} action={false} menu={false} />
		{:else if profile.storage.length === 0}
			<Empty text="No storage probed" />
		{:else}
			<List head={{ primary: 'Mount', secondary: 'Filesystem', instrument: 'Used', facts: ['Holds'] }}>
				{#each profile.storage as s (s.path)}
					<ListRow primary={s.path} primaryKind="path" primaryWidth="24ch" secondary={s.filesystem} secondaryWidth="10ch" facts={[{ value: s.uses.join(', '), tip: s.uses.join('\n') }]}>
						{#snippet instrument()}
							<div class="gauge">
								<span class="track"><Bar segments={storageSegments(s)} total={Number(s.totalBytes)} format={(n) => bytes(n)} /></span>
								<Tip text="{bytes(s.freeBytes)} free" tag="span"><span class="reading">{usedOverTotal(storageUsed(s), s.totalBytes)}</span></Tip>
							</div>
						{/snippet}
					</ListRow>
				{/each}
			</List>
		{/if}
	</Card>
{/snippet}

{#snippet probeRow(p: ProbeResult)}
	<ListRow primary={p.probeId} primaryWidth="18ch" secondary={p.detail} secondaryKind="message" secondaryWidth="40ch" secondaryColor={p.status === ProbeStatus.FAILED ? 'error' : 'muted'}>
		{#snippet leading()}<Mark state={probeState(p)} label={enumLabel(ProbeStatusSchema, p.status)} />{/snippet}
	</ListRow>
{/snippet}

{#snippet systemCard()}
	<Card title="System" flush>
		{#snippet tools()}
			<NeoButton text aria-label="Refresh" loading={refreshing} onclick={refresh}>{#snippet icon()}<ArrowsClockwiseIcon size={20} />{/snippet}</NeoButton>
		{/snippet}
		{#if !profile}
			<Skeleton rows={3} dot={false} leading="20px" primary="16ch" secondary="40ch" facts={0} action={false} menu={false} />
		{:else if probes.length === 0}
			<Empty text="Nothing probed" />
		{:else}
			<List>
				{#each probes as p (p.probeId)}{@render probeRow(p)}{/each}
			</List>
		{/if}
	</Card>
{/snippet}

{#snippet servingRow(inst: Instance)}
	{@const own = tracesOf(inst)}
	<ListRow lamp={instanceLamp(inst.state)} shape={instanceShape(inst.state)} primary={inst.name} primaryWidth="24ch" secondary={modelPhrase(inst)} secondaryKind="repo" secondaryWidth="36ch" href="/serve/{encodeURIComponent(inst.id)}" facts={[{ value: perMinute(own.length / trafficMinutes), tip: `Requests over ${trafficMinutes} minutes` }]}>
		{#snippet action()}
			{#if own.length}<Sparkline points={buckets(own, started, windowFrom, clockTick.now, sparkBuckets, (l) => l.length)} width={96} height={20} />{/if}
		{/snippet}
	</ListRow>
{/snippet}

{#snippet servingCard()}
	<Card title="Serving" flush>
		{#if !live.ready}
			<Skeleton rows={2} primary="24ch" secondary="36ch" facts={1} menu={false} />
		{:else if serving.length === 0}
			<Empty icon={PlayIcon} text="Nothing serving" action="Launch" href="/serve/launch" />
		{:else}
			<List head={{ primary: 'Name', secondary: 'Model', facts: ['Requests'] }}>
				{#each serving as inst (inst.id)}{@render servingRow(inst)}{/each}
			</List>
		{/if}
	</Card>
{/snippet}

{#snippet trafficCard()}
	<Card title="Traffic">
		{#if !tracesSettled}
			<Skeleton rows={1} kind="strip" />
		{:else if tracesError}
			<Empty text="Traffic failed to load" hint={tracesError} action="Retry" onclick={loadTraces} />
		{:else}
			<Facts items={trafficFacts.map((f) => ({ ...f, href: '/traffic?window=15m' }))} />
		{/if}
	</Card>
{/snippet}

{#snippet daemonCard()}
	<Card title="Daemon">
		{#if !gateway && !gatewayError}
			<Skeleton rows={3} kind="facts" />
		{:else}
			<KeyRows
				rows={[
					{ label: 'host', value: profile?.hostname ?? '' },
					{ label: 'system', value: profile ? `${profile.os} ${profile.arch}` : '' },
					{ label: 'probed', value: profile?.probedAt ? when(profile.probedAt) : '' },
					{ label: 'version', value: gateway ? `${gateway.implementation} ${gateway.version}`.trim() : '', mono: true },
					{ label: 'listener', value: gateway?.listeners.map((l) => l.addr).join(', ') ?? '', mono: true, copy: true },
					{ label: 'transport', value: gateway ? (gateway.tls ? 'TLS' : 'plain') : '' },
					{ label: 'auth', value: gateway ? (gateway.auth ? 'on' : 'off') : '' },
					{ label: 'metrics', value: gateway?.metricsAddr ?? '', mono: true },
					{ label: 'home', value: profile?.home ?? '', mono: true, copy: true },
					...facts.map(([k, v]) => ({ label: factLabel(k), value: v, mono: true, tip: v }))
				]}
			/>
			{#if gatewayError}<p class="error-text secondary">{gatewayError}</p>{/if}
		{/if}
	</Card>
{/snippet}

<PageHeader tabs={hostTabs} active={tab} />

{#if tab === 'logs'}
	<Card>
		<LogView source={hostLogs} name={profile?.hostname || 'daemon'} levels height="calc(100dvh - var(--nebu-top, 61px) - 190px)" />
	</Card>
{:else}
	{@render trafficCard()}
	<Columns ratio="3/2">
		{#snippet left()}
			{@render servingCard()}
			{@render devicesCard()}
			{@render storageCard()}
		{/snippet}
		{#snippet right()}
			{@render systemCard()}
			{@render daemonCard()}
		{/snippet}
	</Columns>
{/if}

<style>
	.devlabel {
		display: inline-block;
		min-width: 5ch;
		font-size: var(--nebu-text-secondary);
		font-weight: 500;
	}
	.gauge {
		display: flex;
		flex: 1 1 auto;
		gap: var(--nebu-space-3);
		align-items: center;
		min-width: 0;
	}
	.track {
		display: flex;
		flex: 1 1 auto;
		min-width: 4rem;
	}
	.reading {
		flex: 0 0 auto;
		min-width: 13ch;
		font-size: var(--nebu-text-secondary);
		font-variant-numeric: tabular-nums;
		text-align: right;
		white-space: nowrap;
	}
	.legend {
		padding: var(--nebu-space-3) var(--nebu-card-pad) var(--nebu-space-2);
	}
</style>
