<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { onDestroy, onMount, untrack } from 'svelte';
	import { SvelteMap } from 'svelte/reactivity';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { Code } from '@connectrpc/connect';
	import { isCancel, isCode, reason, rpc } from '#lib/api/transport.js';
	import { bytes, enumLabel, tokensPerSecond } from '#lib/format.js';
	import { live, modelKey } from '#lib/live/events.svelte.js';
	import { PlanProfile, Shape, ShapeSchema, type MemoryPlan, type Part } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { PolicySchema, ProfileSchema } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { RunRequestSchema, type RunRequest } from '#lib/proto/nebu/v1/instance_pb.js';
	import { type FormationPlan } from '#lib/proto/nebu/v1/mesh_pb.js';
	import { ModelKind } from '#lib/proto/nebu/v1/model_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { ModelSort, type StoredModel } from '#lib/proto/nebu/v1/store_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import Collapse from '#lib/ui/Collapse.svelte';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Dot from '#lib/ui/Dot.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import MemoryBars from '#lib/ui/MemoryBars.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import Percent from '#lib/ui/fields/Percent.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import type { Option } from '#lib/ui/fields/types.js';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { deviceLabel } from '#lib/ui/memory.js';
	import { contextLabel, verdictLabel, verdictLamp } from '#lib/pages/models/models.js';
	import ParamsForm from '#lib/pages/serve/ParamsForm.svelte';
	import PolicyFields from '#lib/pages/serve/PolicyFields.svelte';
	import RuntimeFields from '#lib/pages/serve/RuntimeFields.svelte';
	import { keySplits, modelPhrase } from '#lib/pages/serve/serve.js';
	import CubeIcon from 'phosphor-svelte/lib/CubeIcon';
	import EngineIcon from 'phosphor-svelte/lib/EngineIcon';

	export type LaunchStart = {
		slotId?: string;
		swap?: boolean;
		// The key of a stored model, source, repository and group
		model?: string;
		runtimeId?: string;
		params?: Record<string, string>;
		plan?: boolean;
	};

	// One form that launches a model, swaps a slot's model, or plans a formation, a slot's own settings filling it and taking what it launches with
	let { start = {} }: { start?: LaunchStart } = $props();

	const init = untrack(() => start);
	const swap = init.swap === true;
	const planOnly = init.plan === true && !swap;
	// An unpressed group leaves the profile to the planner
	const profiles: Option<PlanProfile>[] = [
		{ value: PlanProfile.CHAT, label: 'Chat' },
		{ value: PlanProfile.AGENT, label: 'Agent' },
		{ value: PlanProfile.BATCH, label: 'Batch' }
	];
	const shapes: Option<Shape>[] = [Shape.SOLO, Shape.CHAIN, Shape.LOCKSTEP, Shape.RELAY, Shape.REPLICAS, Shape.DRAFT, Shape.STAGES].map((s) => ({
		value: s,
		label: enumLabel(ShapeSchema, s),
		description: shapeLine(s)
	}));

	let open = $state(true);
	let left = false;

	let slotId = $state(init.slotId);
	const slot = $derived(slotId ? live.slots.get(slotId) : undefined);
	let modelPick = $state<string | undefined>(undefined);
	const seen = new SvelteMap<string, StoredModel>();
	const model = $derived(modelPick ? (seen.get(modelPick) ?? live.models.get(modelPick)) : undefined);
	const modelId = $derived(model ? modelKey(model) : '');
	const diffusion = $derived(model?.descriptor?.kind === ModelKind.DIFFUSION);
	let parts = $state<Part[]>([]);
	let runtimes = $state<RuntimeStatus[] | undefined>(undefined);
	let runtimeId = $state('');
	// The install the launch runs, the runtime's newest when empty
	let installId = $state('');
	let params = $state<Record<string, string>>({ ...(init.params ?? {}) });
	let args = $state('');
	// Plans against free memory as the launch and the runtime's own fit do, until switched to the whole of it
	let free = $state(true);
	let span = $state<string[]>([]);
	let shape = $state<Shape>(Shape.UNSPECIFIED);
	let profile = $state<PlanProfile>(PlanProfile.AUTO);
	let plan = $state<MemoryPlan | undefined>(undefined);
	let planWarning = $state('');
	let formationPlan = $state<FormationPlan | undefined>(undefined);
	let planError = $state('');
	let name = $state('');
	let defaultName = '';
	let policy = $state(create(PolicySchema));
	let routeProfile = $state(create(ProfileSchema));
	let idleUnload = $state(0);
	let snapshot = $state(false);
	let drainFirst = $state(false);
	let canaryPercent = $state(0);
	let canarySeconds = $state(60);
	let canaryStep = $state(0);
	let maxRegression = $state(10);
	let running = $state(false);
	let runError = $state('');
	let commandOpen = $state(false);
	let preview = $state('');
	let previewError = $state('');
	let planAbort: AbortController | undefined;
	// The slot whose defaults the form last took
	let seeded = '';

	const title = $derived(swap ? `Swap ${slot?.name ?? ''}`.trim() : planOnly ? 'Plan a formation' : 'Launch');
	const inMesh = $derived(live.inMesh);
	const compatible = $derived(
		(runtimes ?? []).filter((r) => {
			const rt = r.runtime;
			if (!rt || !r.compatible) return false;
			if (!model) return true;
			if (model.formatId && !rt.formats.includes(model.formatId)) return false;
			const kind = model.descriptor?.kind ?? ModelKind.UNSPECIFIED;
			if (kind !== ModelKind.UNSPECIFIED && rt.kind !== ModelKind.UNSPECIFIED && rt.kind !== kind) return false;
			return model.runtimes === 0 || (model.runtimes & rt.bit) !== 0;
		})
	);
	// Every install of a runtime that loads the model
	const installs = $derived(compatible.flatMap((r) => [...live.installs.values()].filter((i) => i.runtimeId === r.runtime?.id)));
	// The runtimes that load the model and have an install to run
	const usable = $derived(compatible.filter((r) => installs.some((i) => i.runtimeId === r.runtime?.id)));
	const runtime = $derived(usable.find((r) => r.runtime?.id === runtimeId));
	const runtimeParams = $derived(runtime?.runtime?.params ?? []);

	const nodeDevices = $derived.by((): Option<string>[] => {
		const out: Option<string>[] = [];
		for (const n of live.nodes.values()) {
			out.push({ value: n.id, label: n.name, description: `${n.profile?.devices.length ?? 0} devices` });
			for (const d of n.profile?.devices ?? []) out.push({ value: `${n.id}/${d.id}`, label: `${n.name} ${deviceLabel(n.profile, d)}`, description: d.name });
		}
		return out;
	});
	// Slots holding no model, and the one the launch is headed for
	const slotOptions = $derived(
		[...live.slots.values()]
			.filter((s) => s.instanceId === '' || s.id === slotId)
			.sort((a, b) => a.position - b.position || a.name.localeCompare(b.name))
			.map((s) => ({ value: s.id, label: s.name }))
	);
	const chosenDevices = $derived(slot?.deviceIds.length ? slot.deviceIds : undefined);
	const verdict = $derived(formationPlan?.verdict ?? plan?.verdict);
	const context = $derived(Number(plan?.params['n_ctx'] ?? 0));
	const ready = $derived(model !== undefined && runtime !== undefined && (swap || planOnly || name.trim() !== '') && (!swap || canaryPercent === 0 || canarySeconds > 0));

	function shapeLine(s: Shape): string {
		switch (s) {
			case Shape.SOLO:
				return 'One node';
			case Shape.CHAIN:
				return 'Layer ranges per stage';
			case Shape.LOCKSTEP:
				return 'Every layer on every seat';
			case Shape.RELAY:
				return 'Prefill and decode apart';
			case Shape.REPLICAS:
				return 'Full copy per seat';
			case Shape.DRAFT:
				return 'Draft model on another node';
			default:
				return 'Denoiser apart from encoders';
		}
	}

	function cleanParams(): Record<string, string> {
		return Object.fromEntries(Object.entries(params).filter(([, v]) => v !== ''));
	}

	function request(): RunRequest {
		return create(RunRequestSchema, {
			sourceId: model?.sourceId ?? '',
			repo: model?.repo ?? '',
			group: model?.group ?? '',
			runtimeId,
			installId,
			name: name.trim(),
			params: cleanParams(),
			slotId: slot?.id ?? '',
			// A slot takes the form's settings whole, the ones cleared here cleared on the slot too
			exact: slot !== undefined,
			span,
			shape,
			profile,
			free,
			args,
			policy,
			routeProfile,
			idleUnloadSeconds: idleUnload,
			snapshot
		});
	}

	// Every stored model across every repository, last used first
	async function fetchModels(query: string, cursor: string, limit: number, signal: AbortSignal) {
		const res = await rpc.store.listModels({ query, cursor, limit, sort: ModelSort.LAST_USED }, { signal });
		for (const m of res.models) seen.set(modelKey(m), m);
		return {
			items: res.models.map((m) => ({ value: modelKey(m), label: modelPhrase(m), description: bytes(m.bytes) })),
			next: res.nextCursor
		};
	}

	async function loadModel(want: { sourceId: string; repo: string; group: string }) {
		try {
			const res = await rpc.store.getModel({ sourceId: want.sourceId, repo: want.repo, group: want.group });
			if (!res.model) {
				toast.error('Model failed to load', `${modelPhrase(want)} is not stored`);
				return;
			}
			if (modelPick) return;
			seen.set(modelKey(res.model), res.model);
			modelPick = modelKey(res.model);
		} catch (err) {
			toast.error('Model failed to load', reason(err));
		}
	}

	// Takes the stored model a key names, tried at each split of its repository and group
	async function loadKey(key: string) {
		const known = live.models.get(key);
		if (known) {
			if (!modelPick) modelPick = key;
			return;
		}
		try {
			for (const want of keySplits(key)) {
				const res = await rpc.store.getModel(want).catch((err) => {
					if (isCode(err, Code.NotFound)) return undefined;
					throw err;
				});
				if (!res?.model) continue;
				if (modelPick) return;
				seen.set(modelKey(res.model), res.model);
				modelPick = modelKey(res.model);
				return;
			}
			toast.error('Model failed to load', `${key} is not stored`);
		} catch (err) {
			toast.error('Model failed to load', reason(err));
		}
	}

	onMount(async () => {
		if (init.model) void loadKey(init.model);
		try {
			runtimes = (await rpc.runtimes.listRuntimes({})).runtimes;
		} catch (err) {
			toast.error('Runtimes failed to list', reason(err));
		}
	});

	onDestroy(() => planAbort?.abort());

	// A formation plan spans every node until the operator narrows it
	let spanned = false;
	$effect(() => {
		if (!planOnly || spanned || live.nodes.size === 0) return;
		spanned = true;
		span = [...live.nodes.keys()];
	});

	// Fills the form with a slot's runtime, install, parameters and arguments once it is chosen, and its model when the form holds none
	$effect(() => {
		const s = slot;
		if (!s) {
			seeded = '';
			return;
		}
		if (seeded === s.id) return;
		seeded = s.id;
		untrack(() => {
			params = { ...s.params, ...(init.params ?? {}) };
			args = s.args;
			if (s.runtimeId && !init.runtimeId) {
				runtimeId = s.runtimeId;
				installId = s.installId;
			}
			if (!init.model && !modelPick && s.request?.repo) void loadModel(s.request);
		});
	});

	// Rests on the runtime the link or slot names, else the first that loads the model, and lets go of an install that is gone
	$effect(() => {
		if (runtimes === undefined) return;
		if (!usable.some((r) => r.runtime?.id === runtimeId)) {
			const wanted = init.runtimeId || slot?.runtimeId || '';
			runtimeId = (usable.find((r) => r.runtime?.id === wanted) ?? usable[0])?.runtime?.id ?? '';
			installId = '';
		} else if (installId !== '' && !installs.some((i) => i.id === installId && i.runtimeId === runtimeId)) {
			installId = '';
		}
	});

	// Parameters of another runtime leave with it, so a launch never hands one runtime another's
	let paramsOf = '';
	$effect(() => {
		const rt = runtime?.runtime;
		if (!rt || rt.id === paramsOf) return;
		paramsOf = rt.id;
		const known = new Set(rt.params.map((p) => p.name));
		untrack(() => {
			const kept = Object.fromEntries(Object.entries(params).filter(([k]) => known.has(k)));
			if (Object.keys(kept).length !== Object.keys(params).length) params = kept;
		});
	});

	// The files the model needs that this host lacks, read again whenever the store changes
	$effect(() => {
		const key = modelId;
		parts = [];
		if (!key || !diffusion) return;
		const m = untrack(() => model);
		if (!m) return;
		const abort = new AbortController();
		const load = () =>
			rpc.estimate
				.parts({ sourceId: m.sourceId, repo: m.repo, group: m.group }, { signal: abort.signal })
				.then((r) => (parts = r.parts.filter((p) => p.required && !p.stored && !p.bundled)))
				.catch((err) => {
					if (!isCancel(err)) toast.error('Needed files failed to resolve', reason(err));
				});
		void load();
		const off = live.on(EventKind.MODEL, () => void load());
		return () => {
			off();
			abort.abort();
		};
	});

	async function pullPart(p: Part) {
		try {
			const res = await rpc.store.pull({ sourceId: p.sourceId, repo: p.repo, revision: p.revision, group: p.group, alone: true, paths: p.paths, slot: p.paths.length ? p.slot : '' });
			followTask(res.task, `Pull ${p.name || p.label || p.slot}`);
		} catch (err) {
			toast.error('Pull failed', reason(err));
		}
	}

	// Names the route after the slot, or after the repository until typed over
	$effect(() => {
		const key = modelId;
		const s = slot;
		if (!key) return;
		untrack(() => {
			const next = s?.name ?? model?.repo.split('/').pop() ?? '';
			if (s || name === '' || name === defaultName) name = next;
			defaultName = next;
		});
	});

	// Plans again a moment after the model, the runtime, a parameter or a memory choice changes
	$effect(() => {
		if (!modelId || runtime === undefined) return;
		void [free, span, shape, profile, slot?.id, runtimeId, installId, JSON.stringify(params)];
		const timer = setTimeout(() => void estimate(), 250);
		return () => clearTimeout(timer);
	});

	// Keeps the model and runtime in the URL
	$effect(() => {
		const want: [string, string][] = [
			['model', modelId],
			['runtime', runtimeId]
		];
		untrack(() => {
			if (left) return;
			const url = new URL(page.url.href);
			const stale = want.filter(([k, v]) => v !== '' && url.searchParams.get(k) !== v);
			if (stale.length === 0) return;
			for (const [k, v] of stale) url.searchParams.set(k, v);
			void goto(`${url.pathname}${url.search}`, { replace: true, reset: false });
		});
	});

	// Asks the daemon for the command while its section is open, a moment after the request changes
	$effect(() => {
		if (!commandOpen || !modelId || runtime === undefined) return;
		const run = request();
		const timer = setTimeout(() => {
			previewError = '';
			void rpc.instances
				.previewRun({ run })
				.then((r) => (preview = r.command.join(' ')))
				.catch((err) => {
					preview = '';
					previewError = reason(err);
				});
		}, 250);
		return () => clearTimeout(timer);
	});

	async function estimate() {
		const m = model;
		if (!m || !runtime) return;
		planAbort?.abort();
		const abort = new AbortController();
		planAbort = abort;
		planError = '';
		try {
			if (inMesh && span.length) {
				const res = await rpc.mesh.planFormation({ run: request() }, { signal: abort.signal });
				formationPlan = res.plan;
				plan = res.plan?.memory;
				planWarning = res.plan?.detail ?? '';
			} else {
				const res = await rpc.estimate.estimate({ sourceId: m.sourceId, repo: m.repo, group: m.group, runtimeId, params: cleanParams(), slotId: slot?.id ?? '', exact: slot !== undefined, free }, { signal: abort.signal });
				plan = res.plan;
				planWarning = res.warning;
				formationPlan = undefined;
			}
		} catch (err) {
			if (!isCancel(err)) planError = reason(err);
		}
	}

	// Starts the launch or swap as a task and leaves for the page that shows what it made
	async function run() {
		if (!ready || running) return;
		running = true;
		runError = '';
		try {
			const req = request();
			if (swap && slot) {
				const canary = canaryPercent > 0;
				const res = await rpc.slots.swap({
					slotId: slot.id,
					run: req,
					drainFirst: !canary && drainFirst,
					canaryPercent: canary ? canaryPercent : 0,
					canarySeconds: canary ? canarySeconds : 0,
					canaryStepPercent: canary ? canaryStep : 0,
					canaryMaxRegressionPercent: canary ? maxRegression : undefined
				});
				followTask(res.task);
				leave(res.instance ? `/serve/${encodeURIComponent(res.instance.id)}` : '/serve');
			} else if (inMesh && span.length) {
				const res = await rpc.mesh.runFormation({ run: req });
				followTask(res.task);
				leave(res.formation ? `/mesh/formations/${encodeURIComponent(res.formation.id)}` : '/mesh/formations');
			} else {
				const res = await rpc.instances.run(req);
				followTask(res.task);
				leave(res.instance ? `/serve/${encodeURIComponent(res.instance.id)}` : '/serve');
			}
		} catch (err) {
			runError = reason(err);
		} finally {
			running = false;
		}
	}

	function leave(href = planOnly ? '/mesh/formations' : '/serve') {
		if (left) return;
		left = true;
		open = false;
		void goto(href, { replace: true });
	}
</script>

{#snippet failure()}
	{#if runError}<span class="error-text">{runError}</span>{/if}
{/snippet}

{#snippet memory()}
	<div class="memory">
		{#if planError}
			<p class="error-text secondary">{planError}</p>
		{:else if !plan}
			<Skeleton rows={2} kind="bars" />
		{:else}
			<MemoryBars profile={live.host} instances={live.instances.values()} slots={live.slots.values()} {plan} only={chosenDevices} legend={false} />
			<div class="verdict">
				<Tip facts={[{ label: 'weights', value: bytes(plan.weightsBytes) }, { label: 'cache', value: bytes(plan.cacheBytes) }, { label: 'overhead', value: bytes(plan.overheadBytes) }, { label: 'host reserve', value: bytes(plan.reserveBytes) }]} tag="span">
					<span class="reading"><Dot lamp={verdictLamp(verdict)} label={verdictLabel(verdict)} />{#if context > 0}<span class="muted">{contextLabel(context)} context</span>{/if}</span>
				</Tip>
				<span class="fill"></span>
				<Switch bind:checked={free} label="Count only free memory" compact />
			</div>
			{#if planWarning}<p class="warning-text secondary">{planWarning}</p>{/if}
		{/if}
	</div>
{/snippet}

{#snippet mesh()}
	<Stack gap={12}>
		<Checklist bind:value={span} options={nodeDevices} label="Nodes and devices" />
		<FieldRow>
			<Select bind:value={() => (shape === Shape.UNSPECIFIED ? undefined : shape), (v) => (shape = v ?? Shape.UNSPECIFIED)} options={shapes} label="Shape" placeholder={formationPlan?.shape ? enumLabel(ShapeSchema, formationPlan.shape) : undefined} clearable />
			<Segmented bind:value={() => (profile === PlanProfile.AUTO ? undefined : profile), (v) => (profile = v ?? PlanProfile.AUTO)} options={profiles} label="Tuned for" clearable />
		</FieldRow>
		{#if formationPlan?.candidates.length}
			<div class="rows">
				{#each formationPlan.candidates as c, i (i)}
					<ListRow
						lamp={verdictLamp(c.verdict)}
						primary={enumLabel(ShapeSchema, c.shape)}
						primaryTip={`score ${c.score.toFixed(2)}\nprefill ${c.prefillSeconds.toFixed(2)} s\ndecode ${(c.decodeSecondsPerToken * 1000).toFixed(1)} ms/token\n${c.reason}`}
						secondary={c.nodeIds.map((id) => live.nodes.get(id)?.name ?? id).join(', ')}
						facts={[{ value: tokensPerSecond(c.tokensPerSecond) }, { value: c.speedup ? `${c.speedup.toFixed(1)}x` : '' }]}
					/>
				{/each}
			</div>
		{/if}
	</Stack>
{/snippet}

<Dialog bind:open {title} width="broad" persistent confirm={planOnly ? undefined : swap ? 'Swap' : 'Launch'} busy={running} disabled={!ready} footer={failure} onconfirm={run} onclose={() => leave()}>
	{#if live.ready && live.models.size === 0 && !model}
		<Empty icon={CubeIcon} text="No models stored" action="Discover" href="/discover" />
	{:else}
		<Picker bind:value={modelPick} fetch={fetchModels} label="Model" current={model ? modelPhrase(model) : undefined} required />
		{#if parts.length}
			<div class="rows">
				{#each parts as p (p.slot)}
					<ListRow lamp="error" primary={p.name || p.label || p.slot} secondary={p.error || [p.repo, p.group].filter(Boolean).join(' ')} secondaryKind={p.error ? 'message' : 'repo'} secondaryColor={p.error ? 'error' : 'muted'} facts={[{ value: p.sizeBytes ? bytes(p.sizeBytes) : '' }]}>
						{#snippet action()}
							{#if !p.error}<RowAction label="Pull" onclick={() => pullPart(p)} />{/if}
						{/snippet}
					</ListRow>
				{/each}
			</div>
		{/if}
		{#if model && runtimes !== undefined && usable.length === 0}
			<Empty icon={EngineIcon} text="No runtime installed for this model" action="Runtimes" href="/runtimes" />
		{:else if model && runtime}
			{#if !swap && !planOnly}
				<FieldRow>
					{#if slotOptions.length}<Select bind:value={slotId} options={slotOptions} label="Slot" info="The launch fills the slot and the slot keeps its runtime, parameters and arguments" clearable />{/if}
					{#if !slot}<Text bind:value={name} label="Name" max={64} required />{/if}
				</FieldRow>
			{/if}
			<RuntimeFields runtimes={usable} bind:runtimeId bind:installId />
			{@render memory()}
			{#if inMesh && planOnly}{@render mesh()}{/if}
			<div class="more">
				<Collapse label="Parameters">
					<Stack gap={24}>
						<ParamsForm params={runtimeParams} bind:values={params} supplied={plan?.params} />
						<Text bind:value={args} label="Extra arguments" max={512} mono />
					</Stack>
				</Collapse>
				{#if swap}
					<Collapse label="Rollout">
						<Stack gap={12}>
							<FieldRow>
								<Switch bind:checked={drainFirst} label="Let open requests finish first" disabled={canaryPercent > 0} />
								<Percent bind:value={canaryPercent} max={99} label="Starting share of traffic" disabled={drainFirst} />
							</FieldRow>
							{#if canaryPercent > 0}
								<FieldRow wide>
									<Duration bind:value={canarySeconds} base="s" max={86_400} label="Time between steps" />
									<Percent bind:value={canaryStep} max={99} label="Traffic moved each step" />
								</FieldRow>
								<FieldRow>
									<Percent bind:value={maxRegression} max={100} label="Roll back when slower by" />
								</FieldRow>
							{/if}
						</Stack>
					</Collapse>
				{:else if !planOnly}
					<Collapse label="Route">
						<Stack gap={24}>
							<FieldRow wide>
								<Duration bind:value={idleUnload} base="s" max={86_400} label="Unload when idle for" emptyZero />
							</FieldRow>
							{#if live.host?.facts['criu']}
								<Switch bind:checked={snapshot} label="Snapshot after load" info="Later launches restore it instead of loading again" />
							{/if}
							<PolicyFields bind:policy bind:profile={routeProfile} />
						</Stack>
					</Collapse>
				{/if}
				{#if inMesh && !planOnly}
					<Collapse label="Mesh">{@render mesh()}</Collapse>
				{/if}
				{#if !planOnly}
					<Collapse label="Command" bind:open={commandOpen}>
						{#if previewError}
							<p class="error-text secondary">{previewError}</p>
						{:else if preview}
							<Screen><code class="cmd">{preview}</code></Screen>
						{:else}
							<Skeleton rows={1} kind="bars" />
						{/if}
					</Collapse>
				{/if}
			</div>
		{/if}
	{/if}
</Dialog>

<style>
	.memory {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		min-width: 0;
		padding-top: var(--nebu-space-2);
	}
	.verdict {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-2) var(--nebu-space-3);
		align-items: center;
		min-width: 0;
	}
	.reading {
		display: inline-flex;
		gap: var(--nebu-space-2);
		align-items: center;
	}
	.more {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.rows {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.cmd {
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
</style>
