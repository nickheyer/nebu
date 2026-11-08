<script lang="ts">
	import { create, toJsonString } from '@bufbuild/protobuf';
	import { onDestroy, onMount, untrack } from 'svelte';
	import { SvelteMap } from 'svelte/reactivity';
	import { fly, type FlyParams } from 'svelte/transition';
	import { motion } from '#lib/ui/motion.svelte.js';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import Collapse from '#lib/ui/Collapse.svelte';
	import NeoDialogStepper from '#lib/neo/floating/dialog/NeoDialogStepper.svelte';
	import { NeoProgressBar } from '#lib/neo/progress/index.js';
	import type { NeoStepperContext, NeoStepperStep } from '#lib/neo/stepper/neo-stepper.model.js';
	import { isCancel, reason, rpc } from '#lib/api/transport.js';
	import { bytes, enumLabel, num, percent, seconds, tokensPerSecond } from '#lib/format.js';
	import { live, modelKey } from '#lib/live/events.svelte.js';
	import { ConditionStatus } from '#lib/proto/nebu/v1/meta_pb.js';
	import { FitVerdict, FitVerdictSchema, Placement, PlanProfile, Shape, ShapeSchema, type MemoryPlan, type ParamState, type Part } from '#lib/proto/nebu/v1/estimate_pb.js';
	import { PolicySchema, ProfileSchema } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { InstanceState, InstanceStateSchema, RunRequestSchema, type RunRequest } from '#lib/proto/nebu/v1/instance_pb.js';
	import { FormationState, type FormationPlan } from '#lib/proto/nebu/v1/mesh_pb.js';
	import { ModelKind } from '#lib/proto/nebu/v1/model_pb.js';
	import type { RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { ModelSort, type StoredModel } from '#lib/proto/nebu/v1/store_pb.js';
	import { TaskState, type Task } from '#lib/proto/nebu/v1/task_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import MemoryBars from '#lib/ui/MemoryBars.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StatePill from '#lib/ui/StatePill.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import InfinityIcon from 'phosphor-svelte/lib/InfinityIcon';
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
	import { instanceLamp, type Lamp } from '#lib/ui/status.js';
	import CubeIcon from 'phosphor-svelte/lib/CubeIcon';
	import DownloadSimpleIcon from 'phosphor-svelte/lib/DownloadSimpleIcon';
	import EngineIcon from 'phosphor-svelte/lib/EngineIcon';
	import ParamsForm from '#lib/pages/serve/ParamsForm.svelte';
	import PolicyFields from '#lib/pages/serve/PolicyFields.svelte';
	import { modelPhrase, policyRows, slotUpdate } from '#lib/pages/serve/serve.js';

	export type WizardStart = {
		slotId?: string;
		swap?: boolean;
		model?: { sourceId: string; repo: string; group: string };
		runtimeId?: string;
		params?: Record<string, string>;
		step?: number;
		plan?: boolean;
	};

	type StepId = 'model' | 'runtime' | 'parameters' | 'placement' | 'route' | 'canary' | 'review';
	type Outcome = 'running' | 'ready' | 'failed';

	let { start = {} }: { start?: WizardStart } = $props();

	const init = untrack(() => start);
	const swap = init.swap === true;
	const planOnly = init.plan === true && !swap;
	const ids: StepId[] = planOnly ? ['model', 'runtime', 'parameters', 'placement'] : swap ? ['model', 'runtime', 'parameters', 'placement', 'canary', 'review'] : ['model', 'runtime', 'parameters', 'placement', 'route', 'review'];
	const last = ids.length - 1;
	const labels: Record<StepId, string> = { model: 'Model', runtime: 'Runtime', parameters: 'Parameters', placement: 'Placement', route: 'Route', canary: 'Canary', review: 'Review' };
	const placements: Option<Placement>[] = [
		{ value: Placement.UNSPECIFIED, label: 'Device first' },
		{ value: Placement.DEVICE, label: 'Device' },
		{ value: Placement.HOST, label: 'Host' }
	];
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
	let placement = $state<Placement>(Placement.UNSPECIFIED);
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
	let task = $state<Task | undefined>(undefined);
	let logLines = $state<string[]>([]);
	let instanceId = $state('');
	let formationId = $state('');
	let launched = $state(false);
	let planAbort: AbortController | undefined;
	let followAbort: AbortController | undefined;
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
			return {
				value: i.id,
				label: `${i.version || (i.path.split('/').pop() ?? '')} ${i.origin || i.path}`.trim(),
				description: unmet.length ? unmet.map((c) => `${c.type} ${c.reason}`).join(', ') : installKind(i.kind),
				tip: unmet[0]?.message
			};
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
	const slotOptions = $derived.by((): Option<'this' | 'new' | 'none'>[] => [
		...(slot ? [{ value: 'this' as const, label: slot.name }] : []),
		{ value: 'new', label: 'New slot' },
		{ value: 'none', label: 'No slot', disabled: placement !== Placement.UNSPECIFIED, tip: placement !== Placement.UNSPECIFIED ? 'Needs a slot' : undefined }
	]);
	const chosenDevices = $derived(slot?.deviceIds.length ? slot.deviceIds : undefined);
	const verdict = $derived(formationPlan?.verdict ?? plan?.verdict);
	const instance = $derived(live.instances.get(instanceId));
	const formation = $derived(live.formations.get(formationId));
	const ended = $derived(task?.state === TaskState.SUCCEEDED || task?.state === TaskState.FAILED || task?.state === TaskState.CANCELED);
	const outcome = $derived.by((): Outcome => {
		if (task?.state === TaskState.FAILED || task?.state === TaskState.CANCELED) return 'failed';
		if (instance?.state === InstanceState.FAILED || formation?.state === FormationState.FAILED) return 'failed';
		if (task?.state === TaskState.SUCCEEDED) return 'ready';
		if (!swap && (instance?.state === InstanceState.READY || formation?.state === FormationState.READY)) return 'ready';
		return 'running';
	});
	const progress = $derived(percent(task?.progress?.done, task?.progress?.total));
	const hits = $derived(instance?.triage.length ? instance.triage : (formation?.seats.flatMap((s) => s.triage) ?? []));
	const failure = $derived(instance?.error || task?.error || formation?.error || '');
	const openHref = $derived(instanceId ? `/serve/${encodeURIComponent(instanceId)}` : formationId ? `/mesh/formations/${encodeURIComponent(formationId)}` : '');
	const phase = $derived(outcome === 'failed' ? 'Failed' : outcome === 'ready' ? 'Ready' : 'Running');

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

	const steps = $derived.by((): NeoStepperStep[] =>
		ids.map((id, i) => ({ id, next: i < last ? reach(i) : false, previous: !running, markProps: { position: (i * 100) / last, color: 'primary', label: viewport.mobile ? String(i + 1) : labels[id] } }))
	);

	function slide(node: Element, props: FlyParams) {
		return motion.enabled ? fly(node, { ...props, duration: 200, delay: 0 }) : { duration: 0 };
	}

	function installKind(kind: number): string {
		return ['', 'adopted', 'prebuilt', 'built'][kind] ?? '';
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

	function verdictLamp(v: FitVerdict | undefined): Lamp {
		return v === FitVerdict.FITS ? 'success' : v === FitVerdict.PARTIAL ? 'warning' : v === FitVerdict.NO ? 'error' : 'muted';
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

	onDestroy(() => {
		planAbort?.abort();
		followAbort?.abort();
	});

	// Takes the slot's defaults once it arrives from the live stream
	$effect(() => {
		const s = slot;
		if (!s || seeded) return;
		seeded = true;
		untrack(() => {
			runtimeId ||= s.runtimeId;
			params = { ...s.params, ...params };
			args ||= s.args;
			placement = s.placement;
			slotChoice = 'this';
			if (!init.model && s.request?.repo) void loadModel(s.request);
		});
	});

	$effect(() => {
		const key = modelId;
		parts = [];
		if (!key || !diffusion) return;
		const m = untrack(() => model);
		if (!m) return;
		const abort = new AbortController();
		rpc.estimate
			.parts({ sourceId: m.sourceId, repo: m.repo, group: m.group }, { signal: abort.signal })
			.then((r) => (parts = r.parts))
			.catch((err) => {
				if (!isCancel(err)) toast.error('Parts failed to resolve', reason(err));
			});
		return () => abort.abort();
	});

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

	// A placement other than device first needs a slot
	$effect(() => {
		if (placement !== Placement.UNSPECIFIED && slotChoice === 'none') slotChoice = 'new';
	});

	// Plans again on entering the parameters or placement step and whenever the plan inputs change
	$effect(() => {
		if (launched || (stepId !== 'parameters' && stepId !== 'placement') || !modelId || runtime === undefined) return;
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
		if (launched || stepId !== 'review' || !modelId || runtime === undefined) return;
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

	async function run() {
		if (!model || !runtime || running) return;
		running = true;
		runError = '';
		try {
			const req = request();
			const target = swap || slotChoice === 'this' ? slot : undefined;
			if (target && placement !== target.placement) await rpc.slots.updateSlot(slotUpdate(target, { placement }));
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
				instanceId = res.instance?.id ?? '';
				task = res.task;
			} else if (inMesh && span.length) {
				const res = await rpc.mesh.runFormation({ run: req });
				formationId = res.formation?.id ?? '';
				task = res.task;
			} else {
				if (slotChoice === 'new') {
					const made = await rpc.slots.createSlot({ name: req.name, runtimeId, params: cleanParams(), policy, profile: routeProfile, placement, args });
					req.slotId = made.slot?.id ?? '';
				}
				const res = await rpc.instances.run(req);
				instanceId = res.instance?.id ?? '';
				task = res.task;
			}
			launched = true;
			if (task) void follow(task.id);
		} catch (err) {
			runError = reason(err);
		} finally {
			running = false;
		}
	}

	async function follow(id: string) {
		followAbort?.abort();
		const abort = new AbortController();
		followAbort = abort;
		try {
			for await (const res of rpc.tasks.watchTask({ id }, { signal: abort.signal })) {
				if (res.task) task = res.task;
				if (res.logs.length) logLines = [...logLines, ...res.logs].slice(-200);
			}
		} catch (err) {
			if (!isCancel(err)) toast.error('Task watch failed', reason(err));
		}
	}

	// Returns to the review step with the request as it was
	function back() {
		followAbort?.abort();
		launched = false;
		task = undefined;
		logLines = [];
		instanceId = '';
		formationId = '';
	}

	function leave(href = planOnly ? '/mesh/formations' : '/serve') {
		if (left) return;
		left = true;
		if (task && !ended) followTask(task);
		open = false;
		void goto(href, { replace: true });
	}

	const reviewRows = $derived([
		{ label: 'model', value: modelPhrase(model) },
		{ label: 'runtime', value: `${runtime?.runtime?.name ?? runtimeId} ${installs.find((i) => i.id === installId)?.version ?? ''}`.trim() },
		{ label: 'parameters', value: Object.entries(cleanParams()).map(([k, v]) => `${k}=${v}`).join(' '), mono: true },
		{ label: 'arguments', value: args, mono: true },
		{ label: 'placement', value: placements.find((p) => p.value === placement)?.label ?? '' },
		{ label: 'plan', value: verdict !== undefined ? enumLabel(FitVerdictSchema, verdict) : '' },
		{ label: 'span', value: span.map((v) => nodeDevices.find((o) => o.value === v)?.label ?? v).join(', ') },
		{ label: 'shape', value: span.length ? (shapes.find((s) => s.value === shape)?.label ?? '') : '' },
		{ label: 'plan profile', value: span.length ? (profiles.find((p) => p.value === profile)?.label ?? '') : '' },
		{ label: 'name', value: swap ? '' : name },
		{ label: 'slot', value: swap || slotChoice === 'this' ? (slot?.name ?? '') : slotChoice === 'new' ? name : '' },
		...(swap ? [] : policyRows(policy)),
		{ label: 'tools', value: swap ? '' : routeProfile.toolServers.join(', ') },
		{ label: 'idle unload', value: swap || !idleUnload ? '' : seconds(idleUnload) },
		{ label: 'snapshot', value: snapshot ? 'after load' : '' },
		{ label: 'swap', value: swap ? (canaryPercent ? `canary ${canaryPercent}%, ${seconds(canarySeconds)} steps, ${canaryStep}% more each` : drainFirst ? 'drain first' : 'blue green') : '' }
	]);
</script>

{#snippet keepsLoaded()}<InfinityIcon size={16} aria-label="Stays loaded" />{/snippet}

{#snippet header()}
	<span class="headline ellipsis">{title}{#if launched}<span class="muted"> · {phase}</span>{/if}</span>
{/snippet}

{#snippet footer(ctx: NeoStepperContext)}
	<div class="footer">
		{#if launched}
			<span class="fill"></span>
			{#if outcome === 'failed'}
				<NeoButton class="primary" elevation={2} onclick={back}>Back</NeoButton>
			{:else}
				{#if openHref}<NeoButton elevation={2} onclick={() => leave(openHref)}>Open</NeoButton>{/if}
				{#if outcome === 'ready'}<NeoButton class="primary" elevation={2} onclick={() => leave()}>Done</NeoButton>{/if}
			{/if}
		{:else}
			{#if runError}<span class="status error-text ellipsis">{runError}</span>{/if}
			<span class="fill"></span>
			<NeoButton text disabled={running} onclick={() => leave()}>Cancel</NeoButton>
			{#if active > 0}<NeoButton elevation={2} disabled={running} onclick={() => ctx.goPrevious()}>Back</NeoButton>{/if}
			{#if active < last}
				<NeoButton class="primary" elevation={2} disabled={!complete(stepId)} onclick={() => ctx.goNext()}>Next</NeoButton>
			{:else if planOnly}
				<NeoButton class="primary" elevation={2} onclick={() => leave()}>Done</NeoButton>
			{:else}
				<NeoButton class="primary" elevation={2} loading={running} disabled={!ids.every(complete)} onclick={run}>{swap ? 'Swap' : 'Run'}</NeoButton>
			{/if}
		{/if}
	</div>
{/snippet}

{#snippet children()}
	<div class="content" class:mobile={viewport.mobile}>
		{#if launched}
			<Stack gap={16}>
				<ListRow lamp={outcome === 'failed' ? 'error' : outcome === 'ready' ? 'success' : 'warning'} shape={outcome === 'running' ? 'half' : 'filled'} primary={name || task?.title || title} secondary={task?.progress?.message || task?.title || ''} secondaryKind="message">
					{#snippet badges()}
						{#if instance}<StatePill label={enumLabel(InstanceStateSchema, instance.state)} lamp={instanceLamp(instance.state)} />{/if}
					{/snippet}
				</ListRow>
				<NeoProgressBar value={outcome === 'running' ? (progress ?? 0) : 100} indeterminate={outcome === 'running' && progress === undefined} elevation={-1} height="6px" width="100%" immediate />
				{#if logLines.length}
					<Screen maxHeight="30vh"><pre class="lines">{logLines.join('\n')}</pre></Screen>
				{/if}
				{#if outcome === 'failed'}
					{#if failure}<p class="error-text">{failure}</p>{/if}
					<div class="rows">
						{#each hits as t (t.id)}
							<ListRow lamp="error" primary={t.summary} primaryKind="text" primaryTip={t.line} primaryWidth="32ch" secondary={t.hint} secondaryKind="message" secondaryWidth="64ch" />
						{/each}
					</div>
				{/if}
				{#if instance?.command.length}
					<Collapse label="Command"><Screen><code class="cmd">{instance.command.join(' ')}</code></Screen></Collapse>
				{/if}
			</Stack>
		{:else if stepId === 'model' && live.ready && live.models.size === 0 && !model}
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
							<ListRow primary={p.label || p.slot} secondary={p.name || p.repo} secondaryKind="repo" facts={p.required ? [{ value: 'required' }] : []}>
								{#snippet badges()}
									<StatePill label={p.stored ? 'stored' : p.bundled ? 'bundled' : 'missing'} lamp={p.stored || p.bundled ? 'success' : 'error'} />
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
						{#if compatible.length <= 4}
							<Segmented bind:value={runtimeId} options={runtimeOptions} label="Runtime" />
						{:else}
							<Select bind:value={runtimeId} options={runtimeOptions} label="Runtime" />
						{/if}
						{#if installs.length}
							<Select bind:value={installId} options={installOptions} label="Install" />
						{/if}
					</FormSection>
					{#if runtime && installs.length === 0}
						<Empty icon={DownloadSimpleIcon} text="{runtime.runtime?.name ?? runtimeId} not installed" action="Install" href="/runtimes/{encodeURIComponent(runtimeId)}/install" />
					{/if}
				</Stack>
			{/if}
		{:else if stepId === 'parameters'}
			<Stack gap={24}>
				<ParamsForm params={runtimeParams} bind:values={params} states={paramStates} />
				<FormSection title="Command">
					<Text bind:value={args} label="Arguments" max={512} mono />
				</FormSection>
			</Stack>
		{:else if stepId === 'placement'}
			<Stack gap={16}>
				<MemoryBars profile={live.host} instances={live.instances.values()} slots={live.slots.values()} {plan} only={chosenDevices} />
				<Row>
					<Cell grow={false}>
						{#if verdict !== undefined}
							<Tip facts={plan ? [{ label: 'weights', value: bytes(plan.weightsBytes) }, { label: 'cache', value: bytes(plan.cacheBytes) }, { label: 'overhead', value: bytes(plan.overheadBytes) }] : undefined}>
								<StatePill label={enumLabel(FitVerdictSchema, verdict)} lamp={verdictLamp(verdict)} />
							</Tip>
						{:else}
							<span class="secondary">{planning ? 'Planning' : 'Not planned'}</span>
						{/if}
					</Cell>
					<Cell grow={false} push>
						<NeoButton text loading={planning} onclick={() => estimate()}>Plan again</NeoButton>
					</Cell>
				</Row>
				{#if planWarning}<p class="warning-text">{planWarning}</p>{/if}
				{#if planError}<p class="error-text">{planError}</p>{/if}
				<FormSection title="Placement">
					<FieldRow>
						<Segmented bind:value={placement} options={placements} label="Placement" />
						<Switch bind:checked={free} label="Against free memory" />
					</FieldRow>
				</FormSection>
				{#if inMesh}
					<FormSection title="Mesh" info="An empty shape or profile leaves the choice to the planner">
						<Checklist bind:value={span} options={nodeDevices} label="Span" />
						<FieldRow>
							<Select bind:value={() => (shape === Shape.UNSPECIFIED ? undefined : shape), (v) => (shape = v ?? Shape.UNSPECIFIED)} options={shapes} label="Shape" clearable />
							<Segmented bind:value={() => (profile === PlanProfile.AUTO ? undefined : profile), (v) => (profile = v ?? PlanProfile.AUTO)} options={profiles} label="Plan profile" clearable />
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
				<FormSection title="Route">
					<FieldRow>
						<Text bind:value={name} label="Name" max={64} required />
						<Select bind:value={slotChoice} options={slotOptions} label="Slot" />
					</FieldRow>
					<FieldRow>
						<Duration bind:value={idleUnload} base="s" max={86_400} label="Idle unload" emptyZero mark={keepsLoaded} />
						{#if live.host?.facts['criu']}
							<Switch bind:checked={snapshot} label="Snapshot after load" />
						{/if}
					</FieldRow>
				</FormSection>
				<PolicyFields bind:policy bind:profile={routeProfile} />
			</Stack>
		{:else if stepId === 'canary'}
			<Stack gap={24}>
				<FormSection title="Swap" info="Draining lets open requests finish before the old occupant stops">
					<FieldRow>
						<Switch bind:checked={drainFirst} label="Drain first" disabled={canaryPercent > 0} />
						<Percent bind:value={canaryPercent} max={99} label="Canary share" disabled={drainFirst} />
					</FieldRow>
				</FormSection>
				<FormSection title="Canary" info="Traffic moves to the newcomer a step at a time and rolls back past the regression limit">
					<FieldRow>
						<Duration bind:value={canarySeconds} base="s" max={86_400} label="Step length" disabled={canaryPercent === 0} />
						<Percent bind:value={canaryStep} max={99} label="Step share" disabled={canaryPercent === 0} />
					</FieldRow>
					<FieldRow>
						<Percent bind:value={maxRegression} max={100} label="Max regression" disabled={canaryPercent === 0} />
					</FieldRow>
				</FormSection>
			</Stack>
		{:else if stepId === 'review'}
			<Stack gap={16}>
				<KeyRows rows={reviewRows} />
				<Collapse label="Command">
					{#if previewError}
						<p class="error-text">{previewError}</p>
					{:else if preview}
						<Screen><code class="cmd">{preview}</code></Screen>
					{:else}
						<Skeleton rows={1} kind="bars" />
					{/if}
				</Collapse>
				<Collapse label="Request">
					<Screen><pre class="lines">{toJsonString(RunRequestSchema, request(), { prettySpaces: 2 })}</pre></Screen>
				</Collapse>
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
	progress={!launched}
	controls={false}
	closable={launched}
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
	.lines {
		margin: 0;
		white-space: pre-wrap;
		overflow-wrap: anywhere;
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
	}
	:global(.launch-wizard .neo-stepper-progress .neo-button) {
		--neo-btn-border-color: transparent;
		--neo-btn-border-color-hover: transparent;
		--neo-btn-border-color-disabled: transparent;

		font-size: var(--nebu-text-secondary);
	}
</style>
