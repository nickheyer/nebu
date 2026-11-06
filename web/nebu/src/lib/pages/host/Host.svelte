<script lang="ts">
	import { onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import { NeoButton, NeoCloseButton } from '#lib/neo/buttons/index.js';
	import Collapse from '#lib/ui/Collapse.svelte';
	import { isCancel, reason, rpc } from '#lib/api/transport.js';
	import { bytes, bytesIn, byteUnit, enumLabel, num, thin } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { ProbeStatus, type Device, type ProbeResult, type Storage } from '#lib/proto/nebu/v1/host_pb.js';
	import { TaskStateSchema, type Task } from '#lib/proto/nebu/v1/task_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Bar, { type Segment } from '#lib/ui/Bar.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import InfoTip from '#lib/ui/InfoTip.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LogView from '#lib/ui/LogView.svelte';
	import MemoryBars from '#lib/ui/MemoryBars.svelte';
	import Menu from '#lib/ui/Menu.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Str from '#lib/ui/Str.svelte';
	import Time from '#lib/ui/Time.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Split from '#lib/ui/layout/Split.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { deviceLabel, type PoolBar } from '#lib/ui/memory.js';
	import { taskLamp } from '#lib/ui/status.js';
	import { active } from '#lib/pages/tasks/tasks.js';

	let { tab = 'profile' }: { tab?: string } = $props();

	const profile = $derived(live.host);
	let refreshing = $state(false);
	let doctor = $state<Task | undefined>(undefined);
	let doctorLines = $state<string[]>([]);
	let expanded = $state<Set<string>>(new Set());
	const stop = new AbortController();
	onDestroy(() => stop.abort());

	const probes = $derived([...(profile?.probes ?? [])].sort((a, b) => Number(b.status === ProbeStatus.FAILED) - Number(a.status === ProbeStatus.FAILED)));
	const shownProbes = $derived(probes.filter((p) => p.status !== ProbeStatus.SKIPPED));
	const skipped = $derived(probes.filter((p) => p.status === ProbeStatus.SKIPPED));
	const facts = $derived(Object.entries(profile?.facts ?? {}).sort(([a], [b]) => a.localeCompare(b)));
	// Devices without memory of their own, which no bar draws
	const loose = $derived((profile?.devices ?? []).filter((d) => !profile?.pools.some((p) => p.id === d.poolId)));
	const doctorRunning = $derived(doctor !== undefined && active(doctor));

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

	// Starts the check task and streams its log under the header until the task ends
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

	function toggle(id: string) {
		const next = new Set(expanded);
		if (next.has(id)) next.delete(id);
		else next.add(id);
		expanded = next;
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

	function deviceRows(d: Device) {
		return [
			{ label: 'id', value: d.id, mono: true, copy: true, tip: d.id },
			{ label: 'vendor', value: d.vendor },
			{ label: 'name', value: d.name, tip: d.name },
			{ label: 'memory', value: d.memoryTotalBytes ? `${bytes(d.memoryTotalBytes - d.memoryFreeBytes)} of ${bytes(d.memoryTotalBytes)}` : 'shared' },
			...Object.entries(d.facts)
				.sort(([a], [b]) => a.localeCompare(b))
				.map(([k, v]) => ({ label: k, value: v, mono: true, tip: v }))
		];
	}

	function used(s: Storage): number {
		return Number(s.totalBytes - s.freeBytes);
	}

	// Used over total in the unit of the total, such as 412 / 1024 GB
	function usedOverTotal(s: Storage): string {
		return `${bytesIn(used(s), s.totalBytes)} / ${bytesIn(s.totalBytes, s.totalBytes)}${thin}${byteUnit(s.totalBytes)}`;
	}

	function usesOf(s: Storage): string {
		return s.uses.map((u) => u.split(/[\\/]/).filter(Boolean).pop() ?? u).join(', ');
	}

	// The store's own bytes apart from the rest of the filesystem's use on the mount holding it
	function storageSegments(s: Storage): Segment[] {
		const total = used(s);
		const store = live.store;
		const own = store && s.uses.includes(store.path) ? Math.min(total, Number(store.blobBytes + store.partialBytes + store.cacheBytes)) : 0;
		const segments: Segment[] = own > 0 ? [{ kind: 'used', label: 'store', bytes: own }, { kind: 'other', label: 'other', bytes: total - own }] : [{ kind: 'used', label: 'used', bytes: total }];
		return [...segments, { kind: 'free', label: 'free', bytes: Number(s.freeBytes) }];
	}

	function probeLamp(p: ProbeResult) {
		return p.status === ProbeStatus.OK ? 'success' : p.status === ProbeStatus.FAILED ? 'error' : 'muted';
	}
</script>

{#snippet header()}
	{#if !profile}
		<Skeleton rows={1} height={40} />
	{:else}
		<Row gap={12}>
			<Cell grow={false} max="24ch"><h1 class="title"><Str value={profile.hostname} weight={600} width="100%" /></h1></Cell>
			<Cell grow={false}><span class="secondary nowrap">{profile.os} {profile.arch}</span></Cell>
			<Cell grow={false}>
				<Row gap={4} wrap={false}><span class="secondary">probed</span><span class="secondary"><Time value={profile.probedAt} /></span></Row>
			</Cell>
			<Cell grow={false}><InfoTip click facts={[{ label: 'home', value: profile.home }, { label: 'facts', value: num(facts.length) }]} /></Cell>
			<Cell grow={false} push>
				<Row gap={8} wrap={false}>
					<NeoButton elevation={2} loading={refreshing} onclick={refresh}>Refresh</NeoButton>
					<NeoButton elevation={2} loading={doctorRunning} onclick={runDoctor}>Doctor</NeoButton>
				</Row>
			</Cell>
		</Row>
	{/if}
	{#if doctor}
		<Stack gap={8}>
			<Row gap={12} wrap={false}>
				<Cell grow={false}><StatePill label={enumLabel(TaskStateSchema, doctor.state)} lamp={taskLamp(doctor.state)} /></Cell>
				<Cell min="8rem"><Str value={doctor.error || doctor.progress?.message || doctor.title} kind="message" color={doctor.error ? 'error' : 'muted'} width="100%" /></Cell>
				{#if !doctorRunning}<Cell grow={false} push><NeoCloseButton text aria-label="Close" onclick={closeDoctor} /></Cell>{/if}
			</Row>
			<Screen maxHeight="30vh">
				{#each doctorLines as line, i (i)}<div class="line">{line}</div>{:else}<span class="dim">probing</span>{/each}
			</Screen>
		</Stack>
	{/if}
{/snippet}

{#snippet storageBar(s: Storage)}
	<Bar segments={storageSegments(s)} total={Number(s.totalBytes)} format={(n) => bytes(n)} />
{/snippet}

{#snippet devices()}
	<Section title="Devices" count={profile?.devices.length} gap={8}>
		{#if !profile}
			<Skeleton rows={2} height={28} />
		{:else}
			<MemoryBars {profile} instances={live.instances.values()} slots={live.slots.values()} {expanded}>
				{#snippet menu(bar)}
					<Menu items={[{ label: expanded.has(bar.id) ? 'Hide facts' : 'Expand facts', onclick: () => toggle(bar.id) }]} />
				{/snippet}
				{#snippet expand(bar)}
					{@const procs = processesOf(bar)}
					<Stack gap={12}>
						{#each bar.devices as d (d.id)}
							<KeyRows dense rows={deviceRows(d)} />
						{/each}
						<Stack gap={0}>
							<span class="label">Processes</span>
							{#each procs as p (`${p.deviceId}:${p.pid}`)}
								<ListRow
									primary={p.instanceName || p.instanceId || 'other'}
									href={p.instanceId ? `/serve/${encodeURIComponent(p.instanceId)}` : undefined}
									facts={[{ value: String(p.pid), tip: 'pid', mono: true }, { value: bytes(p.bytes) }]}
								/>
							{:else}
								<span class="secondary">None holding memory</span>
							{/each}
						</Stack>
					</Stack>
				{/snippet}
			</MemoryBars>
			{#if loose.length}
				<Stack gap={0}>
					{#each loose as d (d.id)}
						<ListRow primary={d.name || d.id} primaryTip={d.id} secondary={deviceLabel(profile, d)} secondaryWidth="6ch" facts={[{ value: d.vendor }, { value: 'shared memory' }]}>
							{#snippet expand()}<KeyRows dense rows={deviceRows(d)} />{/snippet}
						</ListRow>
					{/each}
				</Stack>
			{/if}
		{/if}
	</Section>
{/snippet}

{#snippet storage()}
	<Section title="Storage" count={profile?.storage.length} gap={0}>
		{#if !profile}
			<Skeleton rows={2} height={36} />
		{:else}
			{#each profile.storage as s (s.path)}
				<ListRow
					primary={s.path}
					primaryKind="path"
					secondary={s.filesystem}
					secondaryWidth="8ch"
					facts={[{ value: usedOverTotal(s), tip: `${bytes(s.freeBytes)} free` }, { value: usesOf(s), tip: s.uses.join('\n') }]}
				>
					{#snippet instrument()}{@render storageBar(s)}{/snippet}
				</ListRow>
			{:else}
				<Empty text="No storage probed" />
			{/each}
		{/if}
	</Section>
{/snippet}

{#snippet probeRow(p: ProbeResult)}
	<ListRow lamp={probeLamp(p)} shape={p.status === ProbeStatus.SKIPPED ? 'hollow' : 'filled'} primary={p.probeId} primaryWidth="16ch" secondary={p.detail} secondaryKind="message" secondaryWidth="64ch" />
{/snippet}

{#snippet probesSection()}
	<Section title="Probes" count={profile ? shownProbes.length : undefined} gap={0}>
		{#if !profile}
			<Skeleton rows={3} height={36} />
		{:else}
			{#each shownProbes as p (p.probeId)}{@render probeRow(p)}{/each}
			{#if skipped.length}
				<Collapse label="Skipped" count={skipped.length}>
					<Stack gap={0}>
						{#each skipped as p (p.probeId)}{@render probeRow(p)}{/each}
					</Stack>
				</Collapse>
			{/if}
		{/if}
	</Section>
{/snippet}

<Stack gap={24}>
	<Stack gap={12}>{@render header()}</Stack>
	<PageTabs tabs={[{ id: 'profile', label: 'Profile' }, { id: 'logs', label: 'Logs' }]} active={tab} onchange={(id) => goto(id === 'logs' ? '/host/logs' : '/host')} />
	{#if tab === 'logs'}
		<LogView source={hostLogs} name={profile?.hostname || 'daemon'} levels height="60vh" />
	{:else}
		{#if viewport.ultrawide}
			<Split ratio="2/5">
				{#snippet left()}{@render devices()}{/snippet}
				{#snippet right()}
					{@render storage()}
					{@render probesSection()}
				{/snippet}
			</Split>
		{:else}
			{@render devices()}
			{@render storage()}
			{@render probesSection()}
		{/if}
		{#if profile && facts.length}
			<Collapse label="Facts" count={facts.length}>
				<KeyRows dense rows={facts.map(([k, v]) => ({ label: k, value: v, mono: true, tip: v }))} />
			</Collapse>
		{/if}
	{/if}
</Stack>

<style>
	.title {
		display: flex;
		min-width: 0;
	}
	.line {
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
</style>
