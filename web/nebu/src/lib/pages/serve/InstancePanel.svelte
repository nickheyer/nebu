<script lang="ts">
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, enumLabel, num, uptime, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { InstanceStateSchema, ToolSupportSchema, type Instance, type LoadedAdapter } from '#lib/proto/nebu/v1/instance_pb.js';
	import { ParamType, type Runtime } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Bar from '#lib/ui/Bar.svelte';
	import Conditions from '#lib/ui/Conditions.svelte';
	import Confirm from '#lib/ui/Confirm.svelte';
	import KeyRows, { type KeyRow } from '#lib/ui/KeyRows.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LogView from '#lib/ui/LogView.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import Path from '#lib/ui/fields/Path.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Section from '#lib/ui/layout/Section.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { deviceLabel } from '#lib/ui/memory.js';
	import { instanceLamp, instanceShape } from '#lib/ui/status.js';
	import { installLabel, modelPhrase, terminal } from '#lib/pages/serve/serve.js';

	let { instance, tab, ontab, onclose }: { instance: Instance; tab: string; ontab: (id: string) => void; onclose: () => void } = $props();

	const inst = $derived(instance);
	const id = $derived(inst.id);
	const done = $derived(terminal(inst.state));
	const install = $derived(live.installs.get(inst.installId));
	const inv = $derived(inst.inventory);

	// Errors lead when any, then adapters for a running launch that switches them, processes and the formation
	const tabs = $derived.by(() => {
		const list = [{ id: 'overview', label: 'Overview' }, { id: 'logs', label: 'Logs' }];
		if (!done && inst.adapters) list.push({ id: 'adapters', label: 'Adapters' });
		if (inst.ranks.length > 1) list.push({ id: 'ranks', label: 'Processes' });
		if (inst.seat) list.push({ id: 'seat', label: 'Formation' });
		return inst.error || inst.triage.length ? [{ id: 'triage', label: 'Errors' }, ...list] : list;
	});
	const active = $derived(tabs.some((t) => t.id === tab) ? tab : (tabs[0]?.id ?? 'overview'));

	const rows = $derived.by((): KeyRow[] => [
		{ label: 'model', value: modelPhrase(inst) },
		{ label: 'runtime', value: [runtime?.name || inst.runtimeId, install ? installLabel(install) : ''].filter(Boolean).join(' ') },
		{ label: 'endpoint', value: inst.endpoint, mono: true, copy: inst.endpoint !== '' },
		{ label: 'pid', value: inst.pid ? String(inst.pid) : '', mono: true },
		{ label: 'restarts', value: num(inst.restarts) },
		{ label: 'context', value: inv?.contextLength ? `${num(inv.contextLength)} tokens` : '' },
		{ label: 'quantization', value: inv?.quantization ?? '' },
		{ label: 'accelerator', value: inv?.accelerator ?? '' },
		{ label: 'modalities', value: inv?.modalities.join(', ') ?? '' },
		{ label: 'template', value: inst.template ? `${inst.template.source || inst.template.error}, tools ${enumLabel(ToolSupportSchema, inst.template.tools)}` : '' }
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

	// The runtime the launch runs on, read once for its name and what it declares of each parameter
	let runtime = $state<Runtime | undefined>(undefined);
	let runtimeError = $state('');
	const runtimeOf = $derived(inst.runtimeId);
	$effect(() => {
		const runtimeId = runtimeOf;
		runtime = undefined;
		runtimeError = '';
		rpc.runtimes
			.listRuntimes({})
			.then((res) => (runtime = res.runtimes.find((r) => r.runtime?.id === runtimeId)?.runtime))
			.catch((err) => (runtimeError = reason(err)));
	});

	// The parameters the launch set away from the runtime's defaults, under the names the runtime gives them
	const paramRows = $derived.by((): KeyRow[] => {
		const byName = new Map((runtime?.params ?? []).map((p) => [p.name, p]));
		return Object.entries(inst.params)
			.filter(([name, value]) => {
				const p = byName.get(name);
				return !p || value !== (p.default || (p.type === ParamType.BOOL ? 'false' : ''));
			})
			.map(([name, value]) => ({ label: byName.get(name)?.label || name, value: [value, byName.get(name)?.unit].filter(Boolean).join(' '), mono: true, tip: name }));
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
			clearAdapter();
			await listAdapters(id);
		} catch (err) {
			toast.error('Adapter load failed', reason(err));
		} finally {
			loadingAdapter = false;
		}
	}

	function clearAdapter() {
		adapterName = '';
		adapterPath = '';
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

{#snippet stopButton()}
	<Confirm label="Stop" question="Stop {inst.name}?" danger placement="top-start" onconfirm={stop} />
{/snippet}

<PanelHeader title={inst.name} lamp={instanceLamp(inst.state)} shape={instanceShape(inst.state)} state={enumLabel(InstanceStateSchema, inst.state)} subtitle={inst.readyAt && !done ? `up ${uptime(inst.readyAt, clockTick.now)}` : when(inst.stoppedAt ?? inst.createdAt)} {onclose} />
<PageTabs {tabs} {active} onchange={ontab} />
<div class="body">
	{#if active === 'overview'}
		<Stack gap={16}>
			<KeyRows {rows} />
			{#if poolBars.length}
				<Section title="Memory" level={3}>
					<Stack gap={8}>
						{#each poolBars as b (b.id)}
							<div class="pool">
								<span class="poolid">{b.label}</span>
								<span class="track">
									<Bar segments={[{ kind: 'used', label: 'used', bytes: b.measured }, { kind: 'planned', label: 'planned and not used', bytes: Math.max(0, b.planned - b.measured) }]} total={Math.max(b.capacity, b.planned, b.measured)} format={(n) => bytes(n)} />
								</span>
								<span class="secondary nowrap">{bytes(b.measured)} used of {bytes(b.planned)} planned</span>
							</div>
						{/each}
					</Stack>
				</Section>
			{/if}
			<Conditions conditions={inst.conditions} />
			{#if runtimeError}
				<p class="error-text failure">{runtimeError}</p>
			{:else if runtime && paramRows.length}
				<Section title="Parameters" level={3}>
					<KeyRows dense rows={paramRows} />
				</Section>
			{/if}
			{#if inst.command.length}
				<Section title="Command" level={3}>
					<Screen><code class="cmd">{inst.command.join(' ')}</code></Screen>
				</Section>
			{/if}
		</Stack>
	{:else if active === 'logs'}
		<LogView source={logs} name={inst.name} />
	{:else if active === 'adapters'}
			<Stack gap={24}>
				{#if adapters === undefined}
					<Skeleton rows={2} dot={false} primary="24ch" secondary="40ch" facts={0} menu={false} />
				{:else if adapters.length}
					<List head={{ primary: 'Adapter', secondary: 'Path' }}>
						{#each adapters as a (a.name)}
							<ListRow primary={a.name} secondary={a.path} secondaryKind="path">
								{#snippet action()}<Confirm label="Unload" question="Unload {a.name}?" danger onconfirm={() => unloadAdapter(a.name)} />{/snippet}
							</ListRow>
						{/each}
					</List>
				{/if}
				<FormSection title="Load adapter" info="A LoRA, picked per request by its name">
					<FieldRow>
						<Text bind:value={adapterName} label="Name" max={64} required />
						<Path bind:value={adapterPath} label="File" />
					</FieldRow>
				</FormSection>
			</Stack>
			<SaveBar dirty={adapterName !== '' || adapterPath !== ''} saving={loadingAdapter} label="Load" disabled={!adapterName} leading={stopButton} onsave={loadAdapter} ondiscard={clearAdapter} />
	{:else if active === 'ranks'}
		<List head={{ primary: 'Process', secondary: 'Endpoint' }}>
			{#each inst.ranks as r (r.rank)}
				<ListRow lamp={r.healthy ? 'success' : 'error'} primary="rank {r.rank}" primaryTip="pid {r.pid}{r.port ? `\nport ${r.port}` : ''}" primaryWidth="10ch" secondary={r.endpoint} secondaryKind="address" secondaryWidth="24ch" />
			{/each}
		</List>
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
			{#if inst.seat.peers.length}
				<List head={{ primary: 'Peer', secondary: 'Role', facts: ['Address'] }}>
					{#each inst.seat.peers as p (p.nodeId + p.rank)}
						<ListRow primary={live.nodes.get(p.nodeId)?.name ?? p.nodeId} secondary="{p.role} {p.rank}" secondaryWidth="16ch" facts={[{ value: p.address, mono: true }]} />
					{/each}
				</List>
			{/if}
		</Stack>
	{:else if active === 'triage'}
		<Stack gap={12}>
			{#if inst.error}<p class="error-text failure">{inst.error}</p>{/if}
			{#if inst.triage.length}
				<List head={{ primary: 'Error', secondary: 'Fix' }}>
					{#each inst.triage as t (t.id)}
						<ListRow lamp="error" primary={t.summary} primaryKind="text" primaryTip={t.line} primaryWidth="32ch" secondary={t.hint} secondaryKind="message" secondaryWidth="64ch" />
					{/each}
				</List>
			{/if}
		</Stack>
	{/if}
	{#if !done && active !== 'adapters'}
		<div class="actions">{@render stopButton()}</div>
	{/if}
</div>

<style>
	.body {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-4);
		margin-top: var(--nebu-space-3);
	}
	.actions {
		display: flex;
		justify-content: flex-start;
	}
	.pool {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		min-height: var(--nebu-row-dense);
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
