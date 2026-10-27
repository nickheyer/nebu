<script lang="ts">
  import { api, message } from '$lib/api';
  import { live, cached, modelKey, orderedSlots, runtimeName, startedTask, taskFor, joinedMesh, meshNodes, nodeReady, holdersOf, deviceName } from '$lib/state.svelte';
  import { shapeItems, profileItems, shapeOf, profileOf, shapeLabel, seatLabel, verdictLabel, verdictTone, seconds, tps } from '$lib/mesh';
  import { fail } from '$lib/toast.svelte';
  import { runUi } from '$lib/actions.svelte';
  import { launch, slotOccupied } from '$lib/launch';
  import { createForm } from '$lib/form.svelte';
  import { byName, bytes, params as fmtParams, storage, tail } from '$lib/format';
  import { weightsName } from '$lib/catalog';
  import { isComponent, kindOf, kindLabel, partWord } from '$lib/diffusion';
  import { runsOn, servesWord } from '$lib/runtimes';
  import type { MemoryPlan, Part, ParamState } from '$proto/estimate_pb';
  import { FitVerdict, Shape, PlanProfile, Placement } from '$proto/estimate_pb';
  import type { FormationPlan, StoredSummary, Node } from '$proto/mesh_pb';
  import type { StoredModel } from '$proto/store_pb';
  import type { Slot } from '$proto/slot_pb';
  import { ParamType, type Param } from '$proto/runtime_pb';
  import { DeviceKind } from '$proto/host_pb';
  import { Play, ArrowLeftRight, ChevronRight, Download } from '@lucide/svelte';
  import Dialog from './ui/Dialog.svelte';
  import Button from './ui/Button.svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Choices from './ui/Choices.svelte';
  import Segmented from './ui/Segmented.svelte';
  import Spinner from './ui/Spinner.svelte';
  import State from './ui/State.svelte';
  import Chip from './ui/Chip.svelte';
  import Disclosure from './ui/Disclosure.svelte';
  import TextInput from './ui/TextInput.svelte';
  import PlanView from './PlanView.svelte';
  import CandidateTable from './CandidateTable.svelte';
  import ParamForm from './ParamForm.svelte';

  let pickedKey = $state('');
  let runtimeId = $state('');
  let installId = $state('');
  let name = $state('');
  let slot = $state('');
  let values = $state<Record<string, string>>({});
  let invalidFiles = $state(0);
  let invalidPlacement = $state(0);
  let invalidOther = $state(0);
  let swapMode = $state('overlap');
  let showParams = $state(true);
  let plan = $state<MemoryPlan | null>(null);
  let states = $state<ParamState[]>([]);
  let missing = $state<Part[]>([]);
  let planWarning = $state('');
  let planError = $state('');
  let refusal = $state<unknown>(null);
  let checking = $state(false);
  let generation = 0;
  // Nodes the run may use, the shape asked for, and what the planner weighs
  let span = $state<string[]>([]);
  let shapeId = $state('auto');
  let planProfile = $state('chat');
  let fplan = $state<FormationPlan | null>(null);

  const model = $derived(runUi.model);
  // The picker shows whenever this node belongs to a mesh, a one-member mesh included
  const mesh = $derived(joinedMesh());
  const nodes = $derived(meshNodes());
  const meshMode = $derived(mesh && (span.length > 0 || shapeId !== 'auto'));

  // A model a member holds and this node does not, as the picker shows it
  function remoteModel(s: StoredSummary): StoredModel {
    const mask = cached.runtimes.filter((r) => r.runtime && r.runtime.formats.includes(s.formatId) && r.runtime.kind === s.kind).reduce((a, r) => a | (r.runtime?.bit ?? 0), 0);
    return {
      sourceId: s.sourceId,
      repo: s.repo,
      revision: s.revision,
      commit: s.commit,
      group: s.group,
      formatId: s.formatId,
      bytes: s.bytes,
      runtimes: mask,
      artifacts: [],
      path: '',
      pulledAt: s.pulledAt,
      usedAt: s.usedAt,
      descriptor: { parameterCount: s.parameterCount, architecture: s.architecture, kind: s.kind }
    } as unknown as StoredModel;
  }
  const stored = $derived.by(() => {
    const list = [...live.models.values()];
    if (mesh) {
      const have = new Set(list.map(modelKey));
      for (const n of nodes) {
        if (n.self) continue;
        for (const s of n.stored) {
          const key = `${s.sourceId}/${s.repo}/${s.group}`;
          if (have.has(key)) continue;
          have.add(key);
          list.push(remoteModel(s));
        }
      }
    }
    return list.sort(byName((m) => m.repo + m.group));
  });
  const current = $derived(model ?? (pickedKey ? stored.find((m) => modelKey(m) === pickedKey) : undefined) ?? null);
  // Whether this node holds the model itself
  const local = $derived(!!current && live.models.has(modelKey(current)));
  const holders = $derived(current ? holdersOf(current).filter((n) => !n.self) : []);

  function gpuCount(n: Node): number {
    return n.profile?.devices.filter((d) => d.kind !== DeviceKind.CPU).length ?? 0;
  }
  function toggleNode(id: string, on: boolean) {
    const next = new Set(span);
    if (on) next.add(id);
    else next.delete(id);
    span = nodes.filter((n) => next.has(n.id)).map((n) => n.id);
  }
  const slots = $derived(orderedSlots());
  const selectedSlot = $derived(slot ? live.slots.get(slot) : undefined);
  const swap = $derived(slotOccupied(slot));
  const formatId = $derived(current?.formatId ?? '');
  const kind = $derived(kindOf(current?.descriptor));
  const component = $derived(isComponent(current?.descriptor));
  // Runtimes the model's format and kind match come first, every other stays pickable
  const compatible = $derived(cached.runtimes.filter((r) => r.compatible && !!current && runsOn(current.runtimes, r)));
  const others = $derived(cached.runtimes.filter((r) => !compatible.includes(r)));
  const solved = $derived(Object.fromEntries(Object.entries(plan?.params ?? {}).filter(([, v]) => v !== 'auto')));
  // Prefer the requested runtime, then the slot's, then the first compatible runtime.
  const effectiveRuntime = $derived(runtimeId || selectedSlot?.runtimeId || compatible[0]?.runtime?.id || '');
  const runtime = $derived(cached.runtimes.find((r) => r.runtime?.id === effectiveRuntime)?.runtime);
  const inherited = $derived(selectedSlot?.params ?? {});
  const installs = $derived([...live.installs.values()].filter((i) => i.runtimeId === effectiveRuntime));
  const setCount = $derived(Object.keys(values).length);
  const invalid = $derived(invalidFiles + invalidPlacement + invalidOther);

  // Files load beside the weights, placement decides where bytes land, the rest tunes the runtime
  const placementGroups = new Set(['Context', 'Placement', 'Memory', 'Cache', 'Parallelism']);
  const isFile = (p: Param) => p.type === ParamType.PATH;
  const isPlacement = (p: Param) => !isFile(p) && !p.advanced && placementGroups.has(p.group);
  const isOther = (p: Param) => !isFile(p) && !isPlacement(p);
  const runtimeParams = $derived(runtime?.params ?? []);
  const fileParams = $derived(runtimeParams.filter(isFile));
  const fileSet = $derived(fileParams.filter((p) => values[p.name]).length);
  const placementParams = $derived(runtimeParams.filter(isPlacement));
  const otherParams = $derived(runtimeParams.filter(isOther));
  const otherSet = $derived(otherParams.filter((p) => values[p.name]).length);

  // The slot's reservation in a line, what it serves, which GPUs, the cap, its runtime
  function slotDetail(s: Slot): string {
    const parts = [slotOccupied(s.id) ? `serving ${tail(s.request?.repo ?? '')}` : 'empty'];
    if (s.placement === Placement.HOST) parts.push('RAM only');
    else if (s.deviceIds.length) parts.push(s.deviceIds.map(deviceName).join(', '));
    if (s.memoryBytes) parts.push(`cap ${bytes(s.memoryBytes, 0)}`);
    if (s.runtimeId) parts.push(runtimeName(s.runtimeId));
    return parts.join(' · ');
  }

  function spec() {
    return {
      sourceId: current!.sourceId,
      repo: current!.repo,
      group: current!.group,
      runtimeId,
      installId,
      name: slot ? '' : name,
      params: values,
      slotId: slot,
      span: meshMode ? span : [],
      shape: meshMode ? shapeOf(shapeId) : Shape.UNSPECIFIED,
      profile: meshMode ? profileOf(planProfile) : PlanProfile.UNSPECIFIED
    };
  }

  // Discard plans computed for stale inputs.
  async function check() {
    if (!current) return;
    const gen = ++generation;
    checking = true;
    try {
      const s = spec();
      if (meshMode) {
        const resp = await api.mesh.planFormation({ run: { sourceId: s.sourceId, repo: s.repo, group: s.group, runtimeId: effectiveRuntime, installId: '', name: '', params: s.params, slotId: s.slotId, span: s.span, shape: s.shape, profile: s.profile } });
        if (gen !== generation) return;
        fplan = resp.plan ?? null;
        plan = null;
        states = [];
        missing = [];
        planWarning = '';
        return;
      }
      if (!local) {
        // The model sits on other members, so the solo plan waits for a pull or a span
        fplan = null;
        plan = null;
        return;
      }
      const resp = await api.estimate.estimate({ sourceId: s.sourceId, repo: s.repo, group: s.group, runtimeId: effectiveRuntime, params: s.params, slotId: s.slotId, free: true });
      if (gen !== generation) return;
      fplan = null;
      plan = resp.plan ?? null;
      states = resp.params;
      missing = resp.missing;
      planWarning = resp.warning;
    } catch (err) {
      if (gen === generation) planError = message(err);
    } finally {
      if (gen === generation) checking = false;
    }
  }

  // Debounce replanning after input changes. Refresh host memory every ten seconds.
  const replanEvery = 10_000;
  let planFor = '';
  $effect(() => {
    void slot;
    void values;
    void span;
    void shapeId;
    void planProfile;
    // Newly stored parts can resolve automatic file parameters.
    void live.models.size;
    const key = `${pickedKey}\0${effectiveRuntime}\0${meshMode}`;
    const ready = runUi.open && !!current && (!!effectiveRuntime || meshMode);
    generation++;
    // The last plan stays up while inputs change. Another model or runtime starts blank.
    if (key !== planFor || !ready) {
      planFor = key;
      plan = null;
      fplan = null;
      states = [];
      missing = [];
      planWarning = '';
    }
    planError = '';
    refusal = null;
    if (!ready) {
      checking = false;
      return;
    }
    checking = true;
    const timer = setTimeout(() => void check(), 350);
    const again = setInterval(() => void check(), replanEvery);
    return () => {
      clearTimeout(timer);
      clearInterval(again);
    };
  });
  $effect(() => {
    if (otherSet > 0) showParams = true;
  });

  const form = createForm({
    open: () => runUi.open,
    close: () => (runUi.open = false),
    reset() {
      slot = runUi.slotId;
      pickedKey = model ? modelKey(model) : stored[0] ? modelKey(stored[0]) : '';
      runtimeId = runUi.runtimeId;
      installId = name = '';
      values = {};
      swapMode = 'overlap';
      showParams = true;
      span = [];
      shapeId = 'auto';
      planProfile = 'chat';
      fplan = null;
    },
    // launch reports errors. Rethrow to keep the dialog open.
    async submit() {
      const id = await launch(spec(), swapMode === 'drain', (err) => {
        refusal = err;
        planError = message(err);
      });
      if (id === undefined) throw refusal;
    }
  });

  const disabled = $derived(!current);
  // What the daemon is likely to answer, said before the run is asked for
  const cautions = $derived.by(() => {
    const out: string[] = [];
    if (!meshMode && current && !local) out.push('Not stored on this node');
    if (!meshMode && effectiveRuntime && !installs.length) out.push(`${runtimeName(effectiveRuntime)} not installed`);
    if (invalid > 0) out.push(`${invalid} invalid ${invalid === 1 ? 'value' : 'values'}`);
    return out;
  });

  const downloadable = $derived(missing.filter((p) => !p.error && !p.stored && !p.bundled));
  const downloadBytes = $derived(downloadable.reduce((n, p) => n + p.sizeBytes, 0n));
  const required = $derived(missing.filter((p) => p.required));
  const pulling = $derived(current ? taskFor('pull', { source: current.sourceId, repo: current.repo, group: current.group }) : undefined);
  async function downloadParts() {
    if (!current) return;
    try {
      const r = await api.store.pull({ sourceId: current.sourceId, repo: current.repo, revision: current.revision, group: current.group });
      startedTask(`Downloading parts of ${tail(current.repo)}`, `Downloaded parts of ${tail(current.repo)}`, downloadable.map((p) => p.name).join(', '), r.task);
    } catch (err) {
      fail(err, 'Download refused');
    }
  }
</script>

{#snippet heading(id: string, title: string, aside?: string)}
  <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
    <h2 {id} class="text-sm font-semibold text-fg">{title}</h2>
    {#if aside}<span class="text-xs text-fg-muted">{aside}</span>{/if}
  </div>
{/snippet}

<Dialog bind:open={runUi.open} size="xl" title={swap ? `Swap into ${selectedSlot?.name}` : selectedSlot ? `Run in ${selectedSlot.name}` : 'Run a model'} description={current ? `${current.repo} · ${weightsName(current.group, current.formatId)}` : undefined}>
  <form
    class="run-form flex min-w-0 flex-col"
    onsubmit={(e) => {
      e.preventDefault();
      if (!disabled && !form.saving) form.run();
    }}
  >
    <div class="run-grid">
      <div class="flex min-w-0 flex-col gap-6">
        {#if !model}
          <Field label="Model" for="run-model" error={stored.length ? undefined : 'No models downloaded'}>
            <Select id="run-model" mono bind:value={pickedKey} disabled={!stored.length} items={stored.map((m) => ({ value: modelKey(m), label: m.repo, detail: weightsName(m.group, m.formatId) }))} />
          </Field>
        {/if}

        {#if current}
          <div class="flex flex-wrap items-center gap-x-2 gap-y-1.5 text-xs text-fg-muted">
            {#if current.descriptor?.parameterCount}<Chip text="{fmtParams(current.descriptor.parameterCount)} params" mono={false} />{/if}
            <Chip text={storage(current.bytes)} mono={false} title="On disk" />
            <Chip text={current.formatId} title="Format" />
            {#if current.descriptor?.architecture}<Chip text={current.descriptor.architecture} title="Architecture" />{/if}
            {#if current.descriptor?.bitsPerWeight}<Chip text="{current.descriptor.bitsPerWeight.toFixed(1)} bits/weight" mono={false} />{/if}
            {#if kindLabel(current.descriptor)}<Chip text={kindLabel(current.descriptor)} mono={false} />{/if}
            {#if !local && holders.length}<span class="text-fg-muted">Stored on {holders.map((n) => n.name || n.id.slice(0, 8)).join(', ')}, not here</span>{/if}
          </div>
          {#if component}
            <div class="note note-warn">This {partWord(current.descriptor)} is a part a diffusion model loads beside itself.</div>
          {/if}

          <section class="flex min-w-0 flex-col gap-4" aria-labelledby="run-where-title">
            {@render heading('run-where-title', 'Target')}
            {#if slots.length}
              <Choices
                label="Target"
                bind:value={slot}
                items={[
                  { id: '', label: 'No slot' },
                  ...slots.map((s) => ({ id: s.id, label: `${s.position}. ${s.name}`, mono: true, detail: slotDetail(s), warn: slotOccupied(s.id) }))
                ]}
              />
            {/if}
            <div class="setup-grid">
              <Field label="Runtime" for="run-runtime">
                <Select
                  id="run-runtime"
                  unset
                  bind:value={runtimeId}
                  items={[
                    { value: '', label: selectedSlot?.runtimeId ? runtimeName(selectedSlot.runtimeId) : (compatible[0]?.runtime?.name ?? '–') },
                    ...compatible.map((rt) => ({ value: rt.runtime?.id ?? '', label: rt.runtime?.name ?? rt.runtime?.id ?? '' })),
                    ...others.map((rt) => ({ value: rt.runtime?.id ?? '', label: rt.runtime?.name ?? rt.runtime?.id ?? '', detail: !rt.compatible ? 'incompatible' : rt.runtime?.kind !== kind ? servesWord(rt.runtime?.kind ?? 1) : `no ${formatId}` }))
                  ]}
                />
              </Field>
              {#if !meshMode}
                <Field label="Install" for="run-install" error={effectiveRuntime && !installs.length ? 'Not installed' : undefined}>
                  <Select id="run-install" unset bind:value={installId} disabled={!installs.length} items={[{ value: '', label: installs.length ? installs[0].version || installs[0].id : '–' }, ...installs.map((i) => ({ value: i.id, label: i.version || i.id }))]} />
                </Field>
              {/if}
              {#if !slot}
                <Field label="Model name" for="run-name" class="col-span-full">
                  <TextInput id="run-name" mono bind:value={name} empty="{tail(current.repo)}:{current.group}" />
                </Field>
              {/if}
            </div>
          </section>

          {#if mesh}
            <section class="flex flex-col gap-3 border-t border-line pt-4" aria-labelledby="run-mesh-title">
              {@render heading('run-mesh-title', 'Mesh nodes', span.length ? `${span.length} selected` : undefined)}
              <div class="divide-y divide-line overflow-hidden rounded-md border border-line bg-sunken/40">
                {#each nodes as n (n.id)}
                  {@const ready = nodeReady(n)}
                  {@const count = gpuCount(n)}
                  <label class="flex min-h-12 cursor-pointer items-center gap-3 px-3 py-2 text-sm hover:bg-raised/30">
                    <input type="checkbox" class="checkbox shrink-0" checked={span.includes(n.id)} onchange={(e) => toggleNode(n.id, e.currentTarget.checked)} />
                    <span class="min-w-0 flex-1">
                      <span class="block truncate text-fg">{n.name || n.id.slice(0, 8)}</span>
                      <span class="block text-xs text-fg-muted">{count} {count === 1 ? 'GPU' : 'GPUs'} · {n.stored.length} {n.stored.length === 1 ? 'model' : 'models'}</span>
                    </span>
                    {#if !ready}<span class="shrink-0 text-xs text-warn">Unreachable</span>{:else if n.self}<span class="shrink-0 text-xs text-fg-muted">This node</span>{/if}
                  </label>
                {/each}
              </div>
              <div class="setup-grid">
                <Field label="Distribution" for="run-shape">
                  <Select id="run-shape" bind:value={shapeId} items={shapeItems.map((x) => ({ value: x.id, label: x.label }))} />
                </Field>
                <Field label="Workload" for="run-profile">
                  <Select id="run-profile" bind:value={planProfile} items={profileItems.map((x) => ({ value: x.id, label: x.label }))} />
                </Field>
              </div>
            </section>
          {/if}

          {#if fileParams.length}
            <section class="flex flex-col gap-3 border-t border-line pt-4" aria-labelledby="run-files-title">
              <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
                <h2 id="run-files-title" class="text-sm font-semibold text-fg">Files</h2>
                {#if fileSet}<span class="text-xs text-fg-muted">{fileSet} set</span>{/if}
                {#if required.length}<State tone="warn" label="{required.length} missing" />{/if}
                <span class="ml-auto shrink-0">
                  {#if pulling}
                    <span class="text-xs text-fg-muted">Downloading…</span>
                  {:else if downloadable.length}
                    <Button size="sm" variant="primary" icon={Download} onclick={() => void downloadParts()}>Download {downloadable.length === 1 ? 'part' : `${downloadable.length} parts`}{downloadBytes > 0n ? ` · ${storage(downloadBytes)}` : ''}</Button>
                  {/if}
                </span>
              </div>
              <ParamForm params={runtimeParams} filter={isFile} extras={false} flat advancedLabel="More files" model={local ? current : null} bind:values bind:invalid={invalidFiles} {inherited} {states} {solved} {missing} idPrefix="run" />
            </section>
          {/if}

          {#if otherParams.length || !runtimeParams.length}
            <section class="border-t border-line pt-4" aria-labelledby="run-params-title">
              <h2 id="run-params-title">
                <button type="button" class="flex w-full items-center gap-2 rounded-sm text-left text-sm text-fg" aria-expanded={showParams} aria-controls="run-parameters" onclick={() => (showParams = !showParams)}>
                  <ChevronRight size={14} class="shrink-0 text-fg-muted transition-transform {showParams ? 'rotate-90' : ''}" />
                  <span class="font-semibold">Parameters</span>
                  {#if otherSet}<span class="ml-auto text-xs font-normal text-fg-muted">{otherSet} set</span>{/if}
                </button>
              </h2>
              <div id="run-parameters" class="mt-3" hidden={!showParams}>
                <ParamForm params={runtimeParams} filter={isOther} model={local ? current : null} bind:values bind:invalid={invalidOther} {inherited} {states} {solved} {missing} idPrefix="run" />
              </div>
            </section>
          {/if}

          {#if swap}
            <section class="border-t border-line pt-4" aria-labelledby="run-swap-title">
              <div class="flex flex-wrap items-center justify-between gap-3">
                <h2 id="run-swap-title" class="text-sm font-semibold text-fg">Swap</h2>
                <Segmented bind:value={swapMode} tabs={[{ id: 'overlap', label: 'Side by side' }, { id: 'drain', label: 'Stop first' }]} />
              </div>
            </section>
          {/if}
        {/if}
      </div>

      {#if current && (effectiveRuntime || meshMode)}
        <aside class="run-aside">
          <section class="flex flex-col gap-4 rounded-lg border border-line bg-sunken/40 p-4" aria-labelledby="run-placement-title" aria-busy={checking}>
            <div class="flex flex-wrap items-center gap-2">
              <h2 id="run-placement-title" class="text-sm font-semibold text-fg">{meshMode ? 'Run plan' : 'Placement'}</h2>
              {#if selectedSlot}<span class="truncate text-xs text-fg-muted">{selectedSlot.name}</span>{/if}
              {#if checking}<Spinner size={13} class="ml-auto text-fg-muted" />{/if}
            </div>
            {#if meshMode && fplan}
              <div class="flex flex-wrap items-center justify-between gap-2">
                <span class="text-sm font-medium text-fg">{shapeLabel(fplan.shape)}</span>
                <State tone={verdictTone(fplan.verdict)} label={verdictLabel(fplan.verdict)} />
              </div>
              {#if fplan.verdict !== FitVerdict.FITS && fplan.detail}<p class="text-sm leading-5 text-fg-muted wrap-anywhere">{fplan.detail}</p>{/if}
              {#if fplan.prefillSeconds > 0 || fplan.tokensPerSecond > 0}
                <dl class="flex flex-wrap gap-x-6 gap-y-2 text-xs tabular-nums">
                  {#if fplan.prefillSeconds > 0}<div class="flex gap-2"><dt class="text-fg-muted">Est. first token</dt><dd class="text-fg">{seconds(fplan.prefillSeconds)}</dd></div>{/if}
                  {#if fplan.tokensPerSecond > 0}<div class="flex gap-2"><dt class="text-fg-muted">Est. tokens/s</dt><dd class="text-fg">{tps(fplan.tokensPerSecond)}</dd></div>{/if}
                </dl>
              {/if}
              {#if fplan.seats.length}
                <ul class="divide-y divide-line border-y border-line">
                  {#each fplan.seats as s, i (i)}
                    <li class="flex flex-wrap items-baseline gap-x-3 gap-y-1 py-2 text-xs">
                      <span class="min-w-0 flex-1 text-fg wrap-anywhere">{s.nodeName || s.nodeId.slice(0, 8)}</span>
                      <span class="text-fg-muted">{seatLabel(s.role, s.rank)}</span>
                      {#if s.layerTo > s.layerFrom}<span class="text-fg-muted tabular-nums">Layers {s.layerFrom}–{s.layerTo - 1}</span>{/if}
                    </li>
                  {/each}
                </ul>
              {/if}
              <Disclosure label="Compare distributions">
                <CandidateTable plan={fplan} compact />
              </Disclosure>
              {#if (fplan.detail && fplan.verdict === FitVerdict.FITS) || fplan.sources.length}
                <Disclosure label="Plan details">
                  {#if fplan.detail && fplan.verdict === FitVerdict.FITS}<p class="text-xs leading-5 text-fg-muted wrap-anywhere">{fplan.detail}</p>{/if}
                  {#if fplan.sources.length}
                    <ul class="mt-2 flex flex-col gap-1 text-xs leading-5 text-fg-muted">
                      {#each fplan.sources as source}<li class="wrap-anywhere">{source}</li>{/each}
                    </ul>
                  {/if}
                </Disclosure>
              {/if}
            {:else if !meshMode && !local}
              <div class="note note-info">Not stored on this node</div>
            {:else if plan}
              <PlanView {plan} compact params={runtimeParams} />
            {:else if checking}
              <div class="flex flex-col gap-4" aria-busy="true">
                <div class="flex gap-3"><div class="skeleton h-4 w-14"></div><div class="skeleton h-4 w-32"></div></div>
                <div class="flex flex-col gap-1.5"><div class="skeleton h-3 w-24"></div><div class="skeleton h-2.5 w-full"></div></div>
                <div class="flex flex-col gap-1.5"><div class="skeleton h-3 w-32"></div><div class="skeleton h-2.5 w-full"></div></div>
                <div class="skeleton h-3 w-3/4"></div>
              </div>
            {/if}
            {#if planError}<div class="note note-bad">{planError}</div>{/if}
            {#if planWarning && !missing.length}<div class="note note-warn">{planWarning}</div>{/if}
            {#if placementParams.length && !meshMode && local}
              <div class="border-t border-line pt-3">
                <ParamForm params={runtimeParams} filter={isPlacement} extras={false} model={current} bind:values bind:invalid={invalidPlacement} {inherited} {states} {solved} {missing} idPrefix="run-place" />
              </div>
            {/if}
          </section>
        </aside>
      {/if}
    </div>
    <button type="submit" class="hidden" aria-hidden="true" tabindex="-1"></button>
  </form>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (runUi.open = false)}>Cancel</Button>
    {#if cautions.length}<span class="min-w-0 truncate text-xs text-warn" title={cautions.join('. ')}>{cautions.join(' · ')}</span>{/if}
    <span class="ml-auto"><Button variant="primary" icon={swap ? ArrowLeftRight : Play} loading={form.saving} {disabled} onclick={form.run}>{swap ? 'Swap' : 'Run'}</Button></span>
  {/snippet}
</Dialog>

<style>
  .run-form {
    container-type: inline-size;
  }

  .run-grid {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 1.5rem;
  }

  .setup-grid {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 1rem;
  }

  @container (min-width: 30rem) {
    .setup-grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
      column-gap: 1.25rem;
    }
  }

  @container (min-width: 54rem) {
    .run-grid {
      grid-template-columns: minmax(0, 1fr) 21rem;
      align-items: start;
    }
    .run-aside {
      position: sticky;
      top: 0;
    }
  }
</style>
