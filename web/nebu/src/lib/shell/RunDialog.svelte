<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoDialog } from '#lib/neo/floating/dialog/index.js';
	import { NeoInput, NeoNativeSelect, NeoSwitch } from '#lib/neo/inputs/index.js';
	import { NeoPill } from '#lib/neo/pill/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { bytes, enumLabel } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import ParamFields from '#lib/shell/ParamFields.svelte';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { FitVerdict, FitVerdictSchema, PlanProfile, type MemoryPlan } from '#lib/proto/nebu/v1/estimate_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import type { StoredModel } from '#lib/proto/nebu/v1/store_pb.js';

	let { open = $bindable(false), runtimes }: { open?: boolean; runtimes: RuntimeStatus[] } = $props();

	let modelKey = $state('');
	let runtimeId = $state('');
	let installId = $state('');
	let name = $state('');
	let args = $state('');
	let params = $state<Record<string, string>>({});
	let advanced = $state(false);
	let free = $state(false);
	let snapshot = $state(false);
	let idleUnload = $state('');
	let profile = $state(PlanProfile.AUTO);
	let plan = $state<MemoryPlan | undefined>(undefined);
	let planWarning = $state('');
	let planning = $state(false);
	let running = $state(false);

	const modelOptions = $derived(live.models.map((m) => ({ value: key(m), label: `${m.repo}${m.group ? ` [${m.group}]` : ''} (${m.formatId || 'unknown format'})` })));
	const model = $derived(live.models.find((m) => key(m) === modelKey));
	const usable = $derived(runtimes.filter((r) => r.compatible && r.runtime && (!model?.formatId || r.runtime.formats.includes(model.formatId))));
	const runtimeOptions = $derived(usable.map((r) => ({ value: r.runtime?.id ?? '', label: r.runtime?.name ?? '' })));
	const runtime = $derived(usable.find((r) => r.runtime?.id === runtimeId)?.runtime);
	const installs = $derived([...live.installs.values()].filter((i) => i.runtimeId === runtimeId));
	const installOptions = $derived([{ value: '', label: 'Newest install' }, ...installs.map((i) => ({ value: i.id, label: `${i.version || 'unversioned'} ${i.path}` }))]);
	const profileOptions = [
		{ value: PlanProfile.AUTO, label: 'Auto' },
		{ value: PlanProfile.CHAT, label: 'Chat' },
		{ value: PlanProfile.AGENT, label: 'Agent' },
		{ value: PlanProfile.BATCH, label: 'Batch' }
	];
	const verdictColor = $derived(plan?.verdict === FitVerdict.FITS ? 'success' : plan?.verdict === FitVerdict.PARTIAL ? 'warning' : 'error');

	function key(m: StoredModel): string {
		return `${m.sourceId}\u0000${m.repo}\u0000${m.group}`;
	}

	function cleanParams(): Record<string, string> {
		return Object.fromEntries(Object.entries(params).filter(([, v]) => v !== '' && v !== undefined));
	}

	$effect(() => {
		if (!usable.some((r) => r.runtime?.id === runtimeId)) runtimeId = usable[0]?.runtime?.id ?? '';
	});

	$effect(() => {
		runtimeId;
		params = {};
		installId = '';
		plan = undefined;
	});

	async function estimate() {
		if (!model || !runtime) return;
		planning = true;
		try {
			const res = await rpc.estimate.estimate({ sourceId: model.sourceId, repo: model.repo, group: model.group, runtimeId: runtime.id, params: cleanParams(), free });
			plan = res.plan;
			planWarning = res.warning;
		} catch (err) {
			toast.error('Estimate failed', reason(err));
		} finally {
			planning = false;
		}
	}

	async function run() {
		if (!model || !runtime) return;
		running = true;
		try {
			const res = await rpc.instances.run({
				sourceId: model.sourceId,
				repo: model.repo,
				group: model.group,
				runtimeId: runtime.id,
				installId,
				name,
				params: cleanParams(),
				args,
				free,
				snapshot,
				profile,
				idleUnloadSeconds: idleUnload ? Number(idleUnload) : 0
			});
			toast.info('Instance starting', res.instance?.name || res.task?.title);
			open = false;
		} catch (err) {
			toast.error('Run failed', reason(err));
		} finally {
			running = false;
		}
	}
</script>

<NeoDialog bind:open modal rounded elevation={3} width="min(40rem, 94vw)" padding="1.5rem">
	<h2 class="dialog-title">Run a model</h2>
	<form class="stack" onsubmit={(e) => { e.preventDefault(); void run(); }}>
		<NeoNativeSelect bind:value={modelKey} options={[{ value: '', label: 'Choose a model' }, ...modelOptions]} label="Model" required />
		<NeoNativeSelect bind:value={runtimeId} options={runtimeOptions} label="Runtime" message={model && !runtimeOptions.length ? `No installed runtime serves ${model.formatId || 'this format'}` : runtime?.description} required />
		{#if installs.length > 1}
			<NeoNativeSelect bind:value={installId} options={installOptions} label="Install" />
		{/if}
		<NeoInput bind:value={name} label="Name" placeholder="named after the model when empty" autocomplete="off" />
		<NeoNativeSelect bind:value={profile} options={profileOptions} label="Plan profile" message="Shapes the memory plan for the expected traffic" />
		<div class="row">
			<NeoSwitch bind:checked={free} label="Plan against free memory" />
			<NeoSwitch bind:checked={snapshot} label="Snapshot after load" />
			<NeoSwitch bind:checked={advanced} label="Show advanced parameters" />
		</div>
		<NeoInput bind:value={idleUnload} type="number" min={0} label="Unload after idle seconds" placeholder="never" autocomplete="off" />
		{#if runtime}
			<ParamFields params={runtime.params} bind:values={params} {advanced} />
		{/if}
		<NeoInput bind:value={args} label="Extra arguments" placeholder="appended to the runtime command" autocomplete="off" />
		{#if plan}
			<div class="stack">
				<div class="row">
					<NeoPill tinted color={verdictColor} label={enumLabel(FitVerdictSchema, plan.verdict)} />
					<span class="muted">weights {bytes(plan.weightsBytes)}, cache {bytes(plan.cacheBytes)}, overhead {bytes(plan.overheadBytes)}</span>
				</div>
				{#each plan.pools as p (p.poolId)}
					<span class="muted">{p.poolId}: {bytes(p.usedBytes)} of {bytes(p.capacityBytes)}</span>
				{/each}
				{#if plan.detail}<span class="muted">{plan.detail}</span>{/if}
				{#if planWarning}<span style="color: var(--neo-color-warning)">{planWarning}</span>{/if}
			</div>
		{/if}
		<div class="dialog-actions">
			<NeoButton text onclick={() => (open = false)} disabled={running}>Cancel</NeoButton>
			<NeoButton text loading={planning} disabled={!model || !runtime} onclick={estimate}>Estimate</NeoButton>
			<NeoButton type="submit" filled color="primary" loading={running} disabled={!model || !runtime}>Run</NeoButton>
		</div>
	</form>
</NeoDialog>
