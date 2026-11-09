<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { NeoButton, NeoCloseButton } from '#lib/neo/buttons/index.js';
	import { isCancel, reason, rpc } from '#lib/api/transport.js';
	import { bytes, bytesIn, byteUnit, enumLabel, factLabel, num, plural, thin, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import type { GatewayStatus } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { DeviceKind, PoolKind, ProbeStatus, ProbeStatusSchema, type Device, type ProbeResult, type ProcessUse, type Storage } from '#lib/proto/nebu/v1/host_pb.js';
	import { TaskStateSchema, type Task } from '#lib/proto/nebu/v1/task_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import Bar, { type Segment } from '#lib/ui/Bar.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow, { type Fact } from '#lib/ui/ListRow.svelte';
	import LogView from '#lib/ui/LogView.svelte';
	import Mark from '#lib/ui/Mark.svelte';
	import MemoryLegend from '#lib/ui/MemoryLegend.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import Columns from '#lib/ui/layout/Columns.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { poolBars, poolTitle, type PoolBar } from '#lib/ui/memory.js';
	import { taskLamp } from '#lib/ui/status.js';
	import { hostTabs } from '#lib/pages/host/tabs.js';
	import { active } from '#lib/pages/tasks/tasks.js';

	let { tab = 'overview' }: { tab?: string } = $props();

	const profile = $derived(live.host);
	let refreshing = $state(false);
	let doctor = $state<Task | undefined>(undefined);
	let doctorLines = $state<string[]>([]);
	let allProbes = $state(false);
	let gateway = $state<GatewayStatus | undefined>(undefined);
	let gatewayError = $state('');
	const stop = new AbortController();
	onDestroy(() => stop.abort());

	onMount(() => {
		void rpc.gateway
			.getGatewayStatus({}, { signal: stop.signal })
			.then((r) => (gateway = r.status))
			.catch((err) => {
				if (!isCancel(err)) gatewayError = reason(err);
			});
	});

	const probes = $derived(profile?.probes ?? []);
	const failedProbes = $derived(probes.filter((p) => p.status === ProbeStatus.FAILED));
	const passedProbes = $derived(probes.filter((p) => p.status === ProbeStatus.OK));
	const skippedProbes = $derived(probes.filter((p) => p.status === ProbeStatus.SKIPPED));
	// Host facts beyond the os and arch the system row already reads
	const facts = $derived(
		Object.entries(profile?.facts ?? {})
			.filter(([k]) => k !== 'os' && k !== 'arch')
			.sort(([a], [b]) => a.localeCompare(b))
	);
	const doctorRunning = $derived(doctor !== undefined && active(doctor));

	const bars = $derived(poolBars(profile, live.instances.values(), live.slots.values()));
	const kinds = $derived(bars.flatMap((b) => b.segments.filter((s) => s.bytes > 0).map((s) => s.kind)));

	async function refresh() {
		refreshing = true;
		try {
			await rpc.host.getProfile({ refresh: true });
		} catch (err) {
			toast.error('Probe failed', reason(err));
		} finally {
			refreshing = false;
		}
	}

	// Streams the check task's log over the cards until it ends
	async function runDoctor() {
		try {
			const res = await rpc.host.doctor({});
			if (!res.task) throw new Error('the daemon answered without a task');
			followTask(res.task, 'Doctor');
			doctor = res.task;
			doctorLines = [];
			for await (const w of rpc.tasks.watchTask({ id: res.task.id }, { signal: stop.signal })) {
				if (w.task) doctor = w.task;
				if (w.logs.length) doctorLines = [...doctorLines, ...w.logs];
				if (w.task && !active(w.task)) break;
			}
		} catch (err) {
			if (!isCancel(err)) toast.error('Doctor failed', reason(err));
		}
	}

	function closeDoctor() {
		doctor = undefined;
		doctorLines = [];
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
	<Card title="Devices" info="Each pool of memory with the device that draws on it, the CPU on the host's RAM" flush>
		{#if !profile}
			<Skeleton rows={2} kind="bars" />
		{:else if bars.length === 0}
			<Empty text="No devices probed" action="Refresh" onclick={refresh} loading={refreshing} />
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
			<Empty text="No storage probed" action="Refresh" onclick={refresh} loading={refreshing} />
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

{#snippet probesCard()}
	<Card title="Probes" flush>
		{#snippet tools()}
			{#if passedProbes.length || skippedProbes.length}
				<RowAction label={allProbes ? 'Failed only' : 'Show all'} onclick={() => (allProbes = !allProbes)} />
			{/if}
			<NeoButton elevation={2} loading={refreshing} onclick={refresh}>Refresh</NeoButton>
			<NeoButton elevation={2} loading={doctorRunning} onclick={runDoctor}>Doctor</NeoButton>
		{/snippet}
		{#if !profile}
			<Skeleton rows={3} dot={false} leading="20px" primary="16ch" secondary="40ch" facts={0} action={false} menu={false} />
		{:else if probes.length === 0}
			<Empty text="No probes ran" action="Refresh" onclick={refresh} loading={refreshing} />
		{:else}
			<List>
				{#each failedProbes as p (p.probeId)}{@render probeRow(p)}{/each}
				{#if allProbes}
					{#each passedProbes as p (p.probeId)}{@render probeRow(p)}{/each}
					{#each skippedProbes as p (p.probeId)}{@render probeRow(p)}{/each}
				{:else}
					<ListRow primary="{plural(passedProbes.length, 'probe')} passed" primaryKind="text" secondary={skippedProbes.length ? `${num(skippedProbes.length)} skipped` : ''} secondaryWidth="40ch">
						{#snippet leading()}<Mark state="ok" label="Passed" />{/snippet}
					</ListRow>
				{/if}
			</List>
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

{#snippet doctorCard(task: Task)}
	<Card title="Doctor">
		{#snippet tools()}
			<StatePill label={enumLabel(TaskStateSchema, task.state)} lamp={taskLamp(task.state)} />
			{#if !doctorRunning}<NeoCloseButton text aria-label="Close doctor output" onclick={closeDoctor} />{/if}
		{/snippet}
		<Str value={task.error || task.progress?.message || task.title} kind="message" color={task.error ? 'error' : 'muted'} width="100%" />
		<Screen maxHeight="30vh">
			{#each doctorLines as line, i (i)}<div class="line">{line}</div>{:else}<span class="dim">probing</span>{/each}
		</Screen>
	</Card>
{/snippet}

<PageHeader tabs={hostTabs} active={tab} info={tab === 'logs' ? 'The daemon\'s own log, followed live' : 'What this machine offers, its memory, devices, disks and the probes that found them'} />

{#if tab === 'logs'}
	<Card>
		<LogView source={hostLogs} name={profile?.hostname || 'daemon'} levels height="calc(100dvh - var(--nebu-top, 61px) - 190px)" />
	</Card>
{:else}
	{#if doctor}{@render doctorCard(doctor)}{/if}
	<Columns ratio="3/2">
		{#snippet left()}
			{@render devicesCard()}
			{@render storageCard()}
		{/snippet}
		{#snippet right()}
			{@render probesCard()}
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
	.line {
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
</style>
