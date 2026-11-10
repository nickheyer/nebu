<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { onDestroy, onMount, untrack } from 'svelte';
	import { SvelteMap } from 'svelte/reactivity';
	import { fly, type FlyParams } from 'svelte/transition';
	import { motion } from '#lib/ui/motion.svelte.js';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import NeoDialogStepper from '#lib/neo/floating/dialog/NeoDialogStepper.svelte';
	import type { NeoStepperContext, NeoStepperStep } from '#lib/neo/stepper/neo-stepper.model.js';
	import { isCancel, reason, rpc } from '#lib/api/transport.js';
	import { bytes, enumLabel, num, seconds, tokensPerSecond } from '#lib/format.js';
	import { live, modelKey } from '#lib/live/events.svelte.js';
	import { ConditionStatus } from '#lib/proto/nebu/v1/meta_pb.js';
	import { PlanProfile, Shape, ShapeSchema, type MemoryPlan, type ParamState, type Part } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { PolicySchema, ProfileSchema } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { RunRequestSchema, type RunRequest } from '#lib/proto/nebu/v1/instance_pb.js';
	import { type FormationPlan } from '#lib/proto/nebu/v1/mesh_pb.js';
	import { ModelKind } from '#lib/proto/nebu/v1/model_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { ModelSort, type StoredModel } from '#lib/proto/nebu/v1/store_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import MemoryBars from '#lib/ui/MemoryBars.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import Percent from '#lib/ui/fields/Percent.svelte';
	import Picker from '#lib/ui/fields/Picker.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import type { Option } from '#lib/ui/fields/types.js';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { deviceLabel } from '#lib/ui/memory.js';
	import CubeIcon from 'phosphor-svelte/lib/CubeIcon';
	import DownloadSimpleIcon from 'phosphor-svelte/lib/DownloadSimpleIcon';
	import EngineIcon from 'phosphor-svelte/lib/EngineIcon';
	import ParamsForm from '#lib/pages/serve/ParamsForm.svelte';
	import PolicyFields from '#lib/pages/serve/PolicyFields.svelte';
	import { verdictLabel, verdictLamp } from '#lib/pages/models/models.js';
	import { installLabel, modelPhrase, policyRows } from '#lib/pages/serve/serve.js';
	import ArrowsClockwiseIcon from 'phosphor-svelte/lib/ArrowsClockwiseIcon';

	export type WizardStart = {
		slotId?: string;
		swap?: boolean;
		model?: { sourceId: string; repo: string; group: string };
		runtimeId?: string;
		params?: Record<string, string>;
		step?: number;
		plan?: boolean;
	};

	type StepId = 'model' | 'runtime' | 'parameters' | 'memory' | 'route' | 'canary' | 'review';

	let { start = {} }: { start?: WizardStart } = $props();

	const init = untrack(() => start);
	const swap = init.swap === true;
	const planOnly = init.plan === true && !swap;
	const ids: StepId[] = planOnly ? ['model', 'runtime', 'parameters', 'memory'] : swap ? ['model', 'runtime', 'parameters', 'memory', 'canary', 'review'] : ['model', 'runtime', 'parameters', 'memory', 'route', 'review'];
	const last = ids.length - 1;
	const labels: Record<StepId, string> = { model: 'Model', runtime: 'Runtime', parameters: 'Parameters', memory: 'Memory', route: 'Route', canary: 'Canary', review: 'Review' };
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

	const known = init.model ? (init.runtimeId ? 2 : 1) : 0;
	let active = $state(init.step === undefined ? known : Math.min(Math.max(0, init.step), init.model ? last : 0));
	let open = $state(true);
	let left = false;

	const slot = $derived(init.slotId ? live.slots.get(init.slotId) : undefined);
	let modelPick = $state<string | undefined>(undefined);
	const seen = new SvelteMap<string, StoredModel>();
	const model = $derived(modelPick ? (seen.get(modelPick) ?? live.models.get(modelPick)) : undefined);
	const modelId = $derived(model ? modelKey(model) : '');
	const diffusion = $derived(model?.descriptor?.kind === ModelKind.DIFFUSION);
	let parts = $state<Part[]>([]);
	let runtimes = $state<RuntimeStatus[] | undefined>(undefined);
	let runtimeId = $state(init.runtimeId ?? '');
	let installId = $state('');
	let params = $state<Record<string, string>>({ ...(init.params ?? {}) });
	let args = $state('');
	let free = $state(swap);
	let span = $state<string[]>([]);
	let shape = $state<Shape>(Shape.UNSPECIFIED);
	let profile = $state<PlanProfile>(PlanProfile.AUTO);
	let plan = $state<MemoryPlan | undefined>(undefined);
	let planWarning = $state('');
	let paramStates = $state<ParamState[]>([]);
	let formationPlan = $state<FormationPlan | undefined>(undefined);
	let planning = $state(false);
	let planError = $state('');
	let name = $state('');
	let defaultName = '';
	let policy = $state(create(PolicySchema));
	let routeProfile = $state(create(ProfileSchema));
	let idleUnload = $state(0);
	let snapshot = $state(false);
	let slotChoice = $state<'this' | 'new' | 'none'>('none');
	let drainFirst = $state(false);
	let canaryPercent = $state(0);
	let canarySeconds = $state(60);
	let canaryStep = $state(0);
	let maxRegression = $state(10);
	let running = $state(false);
	let runError = $state('');
	let preview = $state('');
	let previewError = $state('');
	let planAbort: AbortController | undefined;
	let seeded = false;

	const stepId = $derived(ids[active] ?? 'model');
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
	const runtimeOptions = $derived(compatible.map((r) => ({ value: r.runtime?.id ?? '', label: r.runtime?.name ?? '', tip: r.runtime?.description || undefined })));
	const runtime = $derived(compatible.find((r) => r.runtime?.id === runtimeId));
	const runtimeParams = $derived(runtime?.runtime?.params ?? []);
	const installs = $derived(
		[...live.installs.values()].filter((i) => i.runtimeId === runtimeId).sort((a, b) => Number(b.createdAt?.seconds ?? 0n) - Number(a.createdAt?.seconds ?? 0n))
	);
	const installOptions = $derived.by((): Option<string>[] =>
		installs.map((i) => {
			const unmet = (i.status?.conditions ?? []).filter((c) => c.status === ConditionStatus.FALSE);
			return { value: i.id, label: installLabel(i), description: unmet.length ? unmet.map((c) => c.message || c.reason).join(', ') : undefined };
		})
	);
	// A formation plan spans every node until the operator narrows it
	let spanned = false;
	$effect(() => {
		if (!planOnly || spanned || live.nodes.size === 0) return;
		spanned = true;
		span = [...live.nodes.keys()];
	});

	const nodeDevices = $derived.by((): Option<string>[] => {
		const out: Option<string>[] = [];
		for (const n of live.nodes.values()) {
			out.push({ value: n.id, label: n.name, description: `${n.profile?.devices.length ?? 0} devices` });
			for (const d of n.profile?.devices ?? []) out.push({ value: `${n.id}/${d.id}`, label: `${n.name} ${deviceLabel(n.profile, d)}`, description: d.name });
		}
		return out;
	});
	const slotOptions = $derived.by((): Option<'this' | 'new' | 'none'>[] => [...(slot ? [{ value: 'this' as const, label: slot.name }] : []), { value: 'new', label: 'New slot' }, { value: 'none', label: 'No slot' }]);
	const chosenDevices = $derived(slot?.deviceIds.length ? slot.deviceIds : undefined);
	const verdict = $derived(formationPlan?.verdict ?? plan?.verdict);
	// Whether a step holds what the request needs from it
	function complete(id: StepId): boolean {
		switch (id) {
			case 'model':
				return model !== undefined;
			case 'runtime':
				return runtime !== undefined;
			case 'route':
				return name.trim() !== '';
			case 'canary':
				return canaryPercent === 0 || canarySeconds > 0;
			default:
				return true;
		}
	}

	// How many steps ahead of a step are reachable, stopping at the first incomplete one
	function reach(i: number): number {
		let j = i;
		while (j < last && complete(ids[j]!)) j++;
		return j - i;
	}

	// Each mark is numbered and wears its state, done behind the active step, current, or still to come
	const steps = $derived.by((): NeoStepperStep[] =>
		ids.map((id, i) => ({
			id,
			next: i < last ? reach(i) : false,
			previous: !running,
			markProps: {
				position: (i * 100) / last,
				color: i === active ? 'var(--neo-background-color)' : 'primary',
				class: i < active ? 'step-done' : i === active ? 'step-current' : 'step-todo',
				label: viewport.mobile ? String(i + 1) : `${i + 1}\u2007${labels[id]}`
			}
		}))
	);

	function slide(node: Element, props: FlyParams) {
		return motion.enabled ? fly(node, { ...props, duration: 200, delay: 0 }) : { duration: 0 };
	}

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
			slotId: swap || slotChoice === 'this' ? (slot?.id ?? '') : '',
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

	async function fetchModels(query: string, cursor: string, limit: number, signal: AbortSignal) {
		const res = await rpc.store.listModels({ query, cursor, limit, sort: ModelSort.LAST_USED }, { signal });
		for (const m of res.models) seen.set(modelKey(m), m);
		return {
			items: res.models.map((m) => ({ value: modelKey(m), label: modelPhrase(m), description: `${m.formatId} ${bytes(m.bytes)}` })),
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

	onMount(async () => {
		if (init.model) void loadModel(init.model);
		try {
			runtimes = (await rpc.runtimes.listRuntimes({})).runtimes;
		} catch (err) {
			toast.error('Runtimes failed to list', reason(err));
		}
	});

	onDestroy(() => planAbort?.abort());

	// Takes the slot's defaults once it arrives from the live stream
	$effect(() => {
		const s = slot;
		if (!s || seeded) return;
		seeded = true;
		untrack(() => {
			runtimeId ||= s.runtimeId;
			params = { ...s.params, ...params };
			args ||= s.args;
			slotChoice = 'this';
			if (!init.model && s.request?.repo) void loadModel(s.request);
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

	$effect(() => {
		if (runtimes === undefined || compatible.some((r) => r.runtime?.id === runtimeId)) return;
		runtimeId = compatible[0]?.runtime?.id ?? '';
	});

	$effect(() => {
		if (!installs.some((i) => i.id === installId)) installId = installs[0]?.id ?? '';
	});

	// Names the route after the slot or the repository until typed over
	$effect(() => {
		const key = modelId;
		if (!key) return;
		untrack(() => {
			const next = slot?.name ?? model?.repo.split('/').pop() ?? '';
			if (name === '' || name === defaultName) name = next;
			defaultName = next;
		});
	});

	// Plans again on entering the parameters or memory step and whenever the plan inputs change
	$effect(() => {
		if ((stepId !== 'parameters' && stepId !== 'memory') || !modelId || runtime === undefined) return;
		void [free, span, shape, profile, installId];
		untrack(() => void estimate());
	});

	// Keeps the step, model and runtime in the URL
	$effect(() => {
		const want: [string, string][] = [
			['step', String(active)],
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

	// Asks the daemon for the command the review step shows
	$effect(() => {
		if (stepId !== 'review' || !modelId || runtime === undefined) return;
		const run = request();
		preview = '';
		previewError = '';
		void rpc.instances
			.previewRun({ run })
			.then((r) => (preview = r.command.join(' ')))
			.catch((err) => (previewError = reason(err)));
	});

	async function estimate() {
		const m = model;
		if (!m || !runtime) return;
		planAbort?.abort();
		const abort = new AbortController();
		planAbort = abort;
		planning = true;
		planError = '';
		try {
			if (inMesh && span.length) {
				const res = await rpc.mesh.planFormation({ run: request() }, { signal: abort.signal });
				formationPlan = res.plan;
				plan = res.plan?.memory;
				planWarning = res.plan?.detail ?? '';
			} else {
				const res = await rpc.estimate.estimate({ sourceId: m.sourceId, repo: m.repo, group: m.group, runtimeId, params: cleanParams(), slotId: slot?.id ?? '', free }, { signal: abort.signal });
				plan = res.plan;
				planWarning = res.warning;
				paramStates = res.params;
				formationPlan = undefined;
			}
		} catch (err) {
			if (!isCancel(err)) planError = reason(err);
		} finally {
			if (planAbort === abort) planning = false;
		}
	}

	// Starts the launch or swap as a task and leaves for the page that shows what it made
	async function run() {
		if (!model || !runtime || running) return;
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
				if (slotChoice === 'new') {
					const made = await rpc.slots.createSlot({ name: req.name, runtimeId, params: cleanParams(), policy, profile: routeProfile, args });
					req.slotId = made.slot?.id ?? '';
				}
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

	const reviewRows = $derived([
		{ label: 'model', value: modelPhrase(model) },
		{ label: 'runtime', value: `${runtime?.runtime?.name ?? runtimeId} ${installs.find((i) => i.id === installId)?.version ?? ''}`.trim() },
		{ label: 'parameters', value: Object.entries(cleanParams()).map(([k, v]) => `${k}=${v}`).join(' '), mono: true },
		{ label: 'arguments', value: args, mono: true },
		{ label: 'memory', value: verdict !== undefined ? verdictLabel(verdict) : '' },
		{ label: 'span', value: span.map((v) => nodeDevices.find((o) => o.value === v)?.label ?? v).join(', ') },
		{ label: 'shape', value: span.length ? (shapes.find((s) => s.value === shape)?.label ?? '') : '' },
		{ label: 'traffic', value: span.length ? (profiles.find((p) => p.value === profile)?.label ?? '') : '' },
		{ label: 'name', value: swap ? '' : name },
		{ label: 'slot', value: swap || slotChoice === 'this' ? (slot?.name ?? '') : slotChoice === 'new' ? name : '' },
		...(swap ? [] : policyRows(policy)),
		{ label: 'tools', value: swap ? '' : routeProfile.toolServers.join(', ') },
		{ label: 'idle unload', value: swap || !idleUnload ? '' : seconds(idleUnload) },
		{ label: 'snapshot', value: snapshot ? 'after load' : '' },
		{ label: 'swap', value: swap ? (canaryPercent ? `canary ${canaryPercent}%, ${seconds(canarySeconds)} steps, ${canaryStep}% more each` : drainFirst ? 'drain first' : 'blue green') : '' }
	]);
</script>

{#snippet header()}
	<span class="headline ellipsis">{title}</span>
{/snippet}

{#snippet footer(ctx: NeoStepperContext)}
	<div class="footer">
		{#if runError}<span class="status error-text">{runError}</span>{/if}
		<span class="fill"></span>
		<NeoButton text disabled={running} onclick={() => leave()}>Cancel</NeoButton>
		{#if active > 0}<NeoButton elevation={2} disabled={running} onclick={() => ctx.goPrevious()}>Back</NeoButton>{/if}
		{#if active < last}
			<NeoButton class="primary" elevation={2} disabled={!complete(stepId)} onclick={() => ctx.goNext()}>Next</NeoButton>
		{:else if planOnly}
			<NeoButton class="primary" elevation={2} onclick={() => leave()}>Done</NeoButton>
		{:else}
			<NeoButton class="primary" elevation={2} loading={running} disabled={!ids.every(complete)} onclick={run}>{swap ? 'Swap' : 'Launch'}</NeoButton>
		{/if}
	</div>
{/snippet}

{#snippet children()}
	<div class="content" class:mobile={viewport.mobile}>
		{#if stepId === 'model' && live.ready && live.models.size === 0 && !model}
			<Empty icon={CubeIcon} text="No models stored" action="Discover" href="/discover" />
		{:else if stepId === 'model'}
			<Stack gap={16}>
				<Picker bind:value={modelPick} fetch={fetchModels} label="Model" current={model ? modelPhrase(model) : undefined} required />
				{#if model}
					<KeyRows
						dense
						rows={[
							{ label: 'format', value: model.formatId },
							{ label: 'size', value: bytes(model.bytes) },
							{ label: 'architecture', value: model.descriptor?.architecture ?? '' },
							{ label: 'parameters', value: model.descriptor?.parameterCount ? num(model.descriptor.parameterCount) : '' },
							{ label: 'precision', value: model.descriptor?.precision?.label ?? '' }
						]}
					/>
				{/if}
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
			</Stack>
		{:else if stepId === 'runtime'}
			{#if runtimes === undefined}
				<Skeleton rows={2} kind="facts" />
			{:else if compatible.length === 0}
				<Empty icon={EngineIcon} text="No compatible runtime" action="Runtimes" href="/runtimes" />
			{:else}
				<Stack gap={16}>
					<FormSection>
						<Select bind:value={runtimeId} options={runtimeOptions} label="Runtime" />
						{#if installs.length}
							<Select bind:value={installId} options={installOptions} label="Version" />
						{/if}
					</FormSection>
					{#if runtime && installs.length === 0}
						<Empty icon={DownloadSimpleIcon} text="{runtime.runtime?.name ?? runtimeId} not installed" action="Install" href="/runtimes/{encodeURIComponent(runtimeId)}/install" />
					{/if}
				</Stack>
			{/if}
		{:else if stepId === 'parameters'}
			<Stack gap={24}>
				<ParamsForm params={runtimeParams} bind:values={params} states={paramStates} resolved={plan?.params} />
				<FormSection>
					<Text bind:value={args} label="Arguments" info="Added to the runtime's command" max={512} mono />
				</FormSection>
			</Stack>
		{:else if stepId === 'memory'}
			<Stack gap={16}>
				<MemoryBars profile={live.host} instances={live.instances.values()} slots={live.slots.values()} {plan} only={chosenDevices} />
				<Row>
					<Cell grow={false}>
						{#if verdict !== undefined}
							<Tip facts={plan ? [{ label: 'weights', value: bytes(plan.weightsBytes) }, { label: 'cache', value: bytes(plan.cacheBytes) }, { label: 'overhead', value: bytes(plan.overheadBytes) }] : undefined}>
								<StatePill label={verdictLabel(verdict)} lamp={verdictLamp(verdict)} />
							</Tip>
						{/if}
					</Cell>
					<Cell grow={false} push>
						<NeoButton text aria-label="Refresh" loading={planning} onclick={() => estimate()}>{#snippet icon()}<ArrowsClockwiseIcon size={20} />{/snippet}</NeoButton>
					</Cell>
				</Row>
				{#if planWarning}<p class="warning-text">{planWarning}</p>{/if}
				{#if planError}<p class="error-text">{planError}</p>{/if}
				<Switch bind:checked={free} label="Count only free memory" />
				{#if inMesh}
					<FormSection title="Mesh">
						<Checklist bind:value={span} options={nodeDevices} label="Nodes and devices" />
						<FieldRow>
							<Select bind:value={() => (shape === Shape.UNSPECIFIED ? undefined : shape), (v) => (shape = v ?? Shape.UNSPECIFIED)} options={shapes} label="Shape" info="How the model splits across the nodes, chosen for you when empty" placeholder={formationPlan?.shape ? enumLabel(ShapeSchema, formationPlan.shape) : undefined} clearable />
							<Segmented bind:value={() => (profile === PlanProfile.AUTO ? undefined : profile), (v) => (profile = v ?? PlanProfile.AUTO)} options={profiles} label="Traffic" info="What the split is tuned for, learned from past requests when none is picked" clearable />
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
					</FormSection>
				{/if}
			</Stack>
		{:else if stepId === 'route'}
			<Stack gap={24}>
				<FormSection>
					<FieldRow>
						<Text bind:value={name} label="Name" max={64} required />
						<Select bind:value={slotChoice} options={slotOptions} label="Slot" />
					</FieldRow>
					<FieldRow wide>
						<Duration bind:value={idleUnload} base="s" max={86_400} label="Unload when idle for" emptyZero />
						{#if live.host?.facts['criu']}
							<Switch bind:checked={snapshot} label="Snapshot after load" info="Later launches restore it instead of loading again" top />
						{/if}
					</FieldRow>
				</FormSection>
				<PolicyFields bind:policy bind:profile={routeProfile} />
			</Stack>
		{:else if stepId === 'canary'}
			<Stack gap={24}>
				<FormSection>
					<FieldRow>
						<Switch bind:checked={drainFirst} label="Let open requests finish first" disabled={canaryPercent > 0} top />
						<Percent bind:value={canaryPercent} max={99} label="Starting share of traffic" disabled={drainFirst} />
					</FieldRow>
				</FormSection>
				{#if canaryPercent > 0}
					<FormSection title="Canary">
						<FieldRow wide>
							<Duration bind:value={canarySeconds} base="s" max={86_400} label="Time between steps" />
							<Percent bind:value={canaryStep} max={99} label="Traffic moved each step" />
						</FieldRow>
						<FieldRow>
							<Percent bind:value={maxRegression} max={100} label="Roll back when slower by" />
						</FieldRow>
					</FormSection>
				{/if}
			</Stack>
		{:else if stepId === 'review'}
			<Stack gap={16}>
				<KeyRows rows={reviewRows} />
				{#if previewError}
					<p class="error-text">{previewError}</p>
				{:else if preview}
					<Screen><code class="cmd">{preview}</code></Screen>
				{:else}
					<Skeleton rows={1} kind="bars" />
				{/if}
			</Stack>
		{/if}
	</div>
{/snippet}

<NeoDialogStepper
	bind:open
	bind:active
	{steps}
	{header}
	{children}
	after={footer}
	headerProps={{ tag: 'h2' }}
	marks
	progress
	controls={false}
	closable={false}
	swipe={false}
	rounded="md"
	filled
	elevation={2}
	width={viewport.mobile ? undefined : { absolute: '720px', max: 'calc(100vw - 32px)' }}
	padding="20px"
	class="launch-wizard"
	stepperProps={{ in: slide, out: slide }}
	dialogProps={{ full: viewport.mobile, closeOnClickOutside: false, onclose: () => leave() }}
	onClose={() => leave()}
/>

<style>
	.headline {
		display: block;
		min-width: 0;
	}
	.content {
		max-height: min(64dvh, 720px);
		padding: var(--nebu-space-1) var(--nebu-space-1) var(--nebu-space-2);
		overflow-x: hidden;
		overflow-y: auto;
	}
	.content.mobile {
		max-height: calc(100dvh - 160px);
	}
	.rows {
		display: flex;
		flex-direction: column;
	}
	.cmd {
		white-space: pre-wrap;
		overflow-wrap: anywhere;
	}
	:global(.launch-wizard .neo-stepper-progress > .neo-progress-bar) {
		min-width: 0;
	}
	:global(.launch-wizard .neo-floating-stepper-title) {
		min-width: 0;
		font-size: var(--nebu-text-heading);
	}
	.footer {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		min-width: 0;
		padding-top: var(--nebu-space-4);
	}
	.status {
		min-width: 0;
		font-size: var(--nebu-text-secondary);
		overflow-wrap: anywhere;
	}
	:global(.launch-wizard .neo-stepper-progress .neo-button) {
		--neo-btn-border-color: transparent;
		--neo-btn-border-color-hover: transparent;
		--neo-btn-border-color-disabled: transparent;

		font-size: var(--nebu-text-secondary);
	}
	/* A done step is a tinted chip, the current one a filled one, a coming one bare words on the line */
	:global(.launch-wizard .neo-stepper-progress .neo-button.step-done) {
		--neo-btn-bg-color: color-mix(in srgb, var(--neo-color-primary) 16%, var(--neo-background-color));
		--neo-btn-bg-color-hover: color-mix(in srgb, var(--neo-color-primary) 16%, var(--neo-background-color));
		--neo-btn-box-shadow: var(--neo-box-shadow-flat);
		--neo-btn-box-shadow-active: var(--neo-box-shadow-flat);
	}
	:global(.launch-wizard .neo-stepper-progress .neo-button.step-current) {
		--neo-btn-bg-color: var(--neo-color-primary);
		--neo-btn-bg-color-hover: var(--neo-color-primary);
		--neo-btn-text-color-hover: var(--neo-background-color);
		--neo-btn-text-color-active: var(--neo-background-color);
		--neo-btn-box-shadow-active: var(--neo-box-shadow-flat);

		font-weight: 500;
	}
	:global(.launch-wizard .neo-stepper-progress .neo-button.step-todo) {
		--neo-btn-bg-color: var(--neo-background-color);
		--neo-btn-bg-color-hover: var(--neo-background-color);
		--neo-btn-box-shadow: var(--neo-box-shadow-flat);
	}
</style>
