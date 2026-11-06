<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import Collapse from '#lib/ui/Collapse.svelte';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, enumLabel, num, uptime, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { InstanceState, InstanceStateSchema, ToolSupportSchema, type Instance, type LoadedAdapter } from '#lib/proto/nebu/v1/instance_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Bar from '#lib/ui/Bar.svelte';
	import Conditions from '#lib/ui/Conditions.svelte';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows, { type KeyRow } from '#lib/ui/KeyRows.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LogView from '#lib/ui/LogView.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import Path from '#lib/ui/fields/Path.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { deviceLabel } from '#lib/ui/memory.js';
	import { instanceLamp } from '#lib/ui/status.js';
	import { modelPhrase, terminal } from '#lib/pages/serve/serve.js';

	let { instance, tab, ontab, onclose }: { instance: Instance; tab: string; ontab: (id: string) => void; onclose: () => void } = $props();

	const inst = $derived(instance);
	const id = $derived(inst.id);
	const done = $derived(terminal(inst.state));
	const failed = $derived(inst.state === InstanceState.FAILED);
	const install = $derived(live.installs.get(inst.installId));
	const inv = $derived(inst.inventory);

	// Triage leads when the instance failed, ranks show with several, the seat with one
	const tabs = $derived.by(() => {
		const list = [{ id: 'overview', label: 'Overview' }, { id: 'logs', label: 'Logs' }, { id: 'adapters', label: 'Adapters' }];
		if (inst.ranks.length > 1) list.push({ id: 'ranks', label: 'Ranks' });
		if (inst.seat) list.push({ id: 'seat', label: 'Seat' });
		const triage = { id: 'triage', label: 'Triage' };
		return failed ? [triage, ...list] : [...list, triage];
	});
	const active = $derived(tabs.some((t) => t.id === tab) ? tab : (tabs[0]?.id ?? 'overview'));

	const rows = $derived.by((): KeyRow[] => [
		{ label: 'model', value: modelPhrase(inst) },
		{ label: 'runtime', value: `${inst.runtimeId}${install ? ` ${install.version || install.origin}` : ''}` },
		{ label: 'endpoint', value: inst.endpoint, mono: true, copy: inst.endpoint !== '' },
		{ label: 'pid', value: inst.pid ? String(inst.pid) : '', mono: true },
		{ label: 'restarts', value: num(inst.restarts) },
		{ label: 'health', value: inst.lastHealthAt ? `${inst.healthy ? 'healthy' : 'unhealthy'}, probed ${when(inst.lastHealthAt)}` : '' },
		{ label: 'context', value: inv?.contextLength ? `${num(inv.contextLength)} tokens` : '' },
		{ label: 'quantization', value: inv?.quantization ?? '' },
		{ label: 'accelerator', value: inv?.accelerator ?? '' },
		{ label: 'modalities', value: inv?.modalities.join(', ') ?? '' },
		{ label: 'template', value: inst.template ? `${inst.template.source || inst.template.error || 'none'}, tools ${enumLabel(ToolSupportSchema, inst.template.tools)}` : '' }
	]);

	// Planned and measured bytes per pool the plan touches, measured by the processes the host attributes
	const poolBars = $derived.by(() => {
		const plan = inst.plan;
		if (!plan) return [];
		return plan.pools.map((pu) => {
			const devices = (live.host?.devices ?? []).filter((d) => d.poolId === pu.poolId);
			const ids = devices.map((d) => d.id);
			const measured = (live.host?.processes ?? []).filter((p) => p.instanceId === inst.id && ids.includes(p.deviceId)).reduce((n, p) => n + Number(p.bytes), 0);
			const sample = inst.measurements.find((m) => m.key === pu.poolId || m.key === `device.used.${pu.poolId}`);
			return {
				id: pu.poolId,
				label: devices.map((d) => deviceLabel(live.host, d)).join(' ') || pu.poolId,
				planned: Number(pu.usedBytes),
				measured: measured || Number(sample?.bytes ?? 0),
				capacity: Number(pu.capacityBytes)
			};
		});
	});

	let adapters = $state<LoadedAdapter[] | undefined>(undefined);
	let adapterName = $state('');
	let adapterPath = $state('');
	let loadingAdapter = $state(false);

	$effect(() => {
		if (active !== 'adapters' || done) return;
		const target = id;
		adapters = undefined;
		void listAdapters(target);
	});

	async function listAdapters(target: string) {
		try {
			adapters = (await rpc.instances.listAdapters({ id: target })).adapters;
		} catch (err) {
			toast.error('Adapters failed to list', reason(err));
		}
	}

	async function loadAdapter() {
		loadingAdapter = true;
		try {
			await rpc.instances.loadAdapter({ id: inst.id, name: adapterName, path: adapterPath });
			toast.success(`Loaded ${adapterName}`);
			adapterName = '';
			adapterPath = '';
			await listAdapters(id);
		} catch (err) {
			toast.error('Adapter load failed', reason(err));
		} finally {
			loadingAdapter = false;
		}
	}

	async function unloadAdapter(name: string) {
		try {
			await rpc.instances.unloadAdapter({ id: inst.id, name });
			await listAdapters(id);
		} catch (err) {
			toast.error('Adapter unload failed', reason(err));
		}
	}

	async function stop() {
		try {
			await rpc.instances.stopInstance({ id: inst.id });
		} catch (err) {
			toast.error('Stop failed', reason(err));
		}
	}

	// A log source bound to the instance id alone, so live updates of the record keep the stream open
	const logs = $derived.by(() => {
		const target = id;
		return (tail: number, follow: boolean, signal: AbortSignal) => {
			const stream = rpc.instances.logs({ id: target, tail, follow }, { signal });
			return (async function* () {
				for await (const res of stream) yield res.lines;
			})();
		};
	});
</script>

<PanelHeader title={inst.name} subtitle={inst.readyAt && !done ? `up ${uptime(inst.readyAt, clockTick.now)}` : when(inst.stoppedAt ?? inst.createdAt)} {onclose}>
	<StatePill label={enumLabel(InstanceStateSchema, inst.state)} lamp={instanceLamp(inst.state)} />
	{#snippet tools()}
		{#if !done}<Confirm label="Stop" question="Stop {inst.name}?" danger onconfirm={stop} />{/if}
		<NeoButton text onclick={() => ontab('logs')}>Logs</NeoButton>
	{/snippet}
</PanelHeader>
<PageTabs {tabs} {active} onchange={ontab} />
<div class="body">
	{#if active === 'overview'}
		<Stack gap={16}>
			<KeyRows {rows} />
			{#if poolBars.length}
				<Stack gap={8}>
					<span class="label">Planned against measured</span>
					{#each poolBars as b (b.id)}
						<div class="pool">
							<span class="poolid">{b.label}</span>
							<span class="track">
								<Bar segments={[{ kind: 'used', label: 'measured', bytes: b.measured }, { kind: 'planned', label: 'planned', bytes: b.planned }]} total={Math.max(b.capacity, b.planned, b.measured)} format={(n) => bytes(n)} />
							</span>
							<span class="secondary nowrap"><span class="num">{bytes(b.measured)}</span> / <span class="num">{bytes(b.planned)}</span></span>
						</div>
					{/each}
				</Stack>
			{/if}
			<Conditions conditions={inst.conditions} />
			<Collapse label="Parameters">
				<KeyRows dense rows={Object.entries(inst.params).map(([k, v]) => ({ label: k, value: v, mono: true, tip: v }))} />
			</Collapse>
			<Collapse label="Command">
				<Screen><code class="cmd">{inst.command.join(' ')}</code></Screen>
			</Collapse>
		</Stack>
	{:else if active === 'logs'}
		<LogView source={logs} name={inst.name} />
	{:else if active === 'adapters'}
		{#if done}
			<Empty text="Instance stopped" />
		{:else}
			<Stack gap={16}>
				{#if adapters === undefined}
					<Skeleton rows={2} />
				{:else if adapters.length === 0}
					<Empty text="No adapters loaded" />
				{:else}
					<Stack gap={0}>
						{#each adapters as a (a.name)}
							<ListRow primary={a.name} secondary={a.path} secondaryKind="path">
								{#snippet action()}<Confirm label="Unload" question="Unload {a.name}?" danger onconfirm={() => unloadAdapter(a.name)} />{/snippet}
							</ListRow>
						{/each}
					</Stack>
				{/if}
				<FormSection title="Load adapter">
					<FieldRow>
						<Text bind:value={adapterName} label="Name" max={64} />
						<Path bind:value={adapterPath} label="Path" info="Empty loads one the runtime loaded at launch" />
					</FieldRow>
					<Row justify="flex-end">
						<NeoButton class="primary" elevation={2} loading={loadingAdapter} disabled={!adapterName} onclick={loadAdapter}>Load</NeoButton>
					</Row>
				</FormSection>
			</Stack>
		{/if}
	{:else if active === 'ranks'}
		<Stack gap={0}>
			{#each inst.ranks as r (r.rank)}
				<ListRow lamp={r.healthy ? 'success' : 'error'} primary="rank {r.rank}" primaryTip="pid {r.pid}{'\n'}port {r.port || 'none'}" primaryWidth="10ch" secondary={r.endpoint} secondaryKind="address" secondaryWidth="24ch" />
			{/each}
		</Stack>
	{:else if active === 'seat' && inst.seat}
		<Stack gap={16}>
			<KeyRows
				rows={[
					{ label: 'formation', value: inst.seat.name || inst.seat.formationId, href: `/mesh/formations/${encodeURIComponent(inst.seat.formationId)}` },
					{ label: 'role and rank', value: `${inst.seat.role} ${inst.seat.rank}` },
					{ label: 'layers', value: inst.seat.layerTo ? `${inst.seat.layerFrom} to ${inst.seat.layerTo}` : 'every layer' },
					{ label: 'transport', value: inst.transport || 'pending' },
					{ label: 'rendezvous', value: inst.seat.rendezvous, mono: true, copy: inst.seat.rendezvous !== '' }
				]}
			/>
			<Stack gap={0}>
				{#each inst.seat.peers as p (p.nodeId + p.rank)}
					<ListRow primary={live.nodes.get(p.nodeId)?.name ?? p.nodeId} secondary="{p.role} {p.rank}" secondaryWidth="16ch" facts={[{ value: p.address, mono: true }]} />
				{/each}
			</Stack>
		</Stack>
	{:else if active === 'triage'}
		<Stack gap={12}>
			{#if inst.error}<p class="error-text failure">{inst.error}</p>{/if}
			{#if !inst.error && inst.triage.length === 0}<Empty text="No triage hits" />{/if}
			<Stack gap={0}>
				{#each inst.triage as t (t.id)}
					<ListRow lamp="error" primary={t.summary} primaryKind="text" primaryTip={t.line} primaryWidth="32ch" secondary={t.hint} secondaryKind="message" secondaryWidth="64ch" />
				{/each}
			</Stack>
		</Stack>
	{/if}
</div>

<style>
	.body {
		margin-top: 12px;
	}
	.pool {
		display: flex;
		gap: 12px;
		align-items: center;
		min-height: 28px;
	}
	.poolid {
		flex: 0 0 10ch;
	}
	.track {
		display: flex;
		flex: 1 1 auto;
		min-width: 0;
	}
	.cmd,
	.failure {
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
</style>
