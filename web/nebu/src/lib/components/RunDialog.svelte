<script lang="ts">
  import { Code } from '@connectrpc/connect';
  import { api, code, message } from '$lib/api';
  import { live, cached, modelKey, orderedSlots, runtimeName } from '$lib/state.svelte';
  import { runUi } from '$lib/actions.svelte';
  import { launch, slotOccupied } from '$lib/launch';
  import { createForm } from '$lib/form.svelte';
  import { byName, bytes, params as fmtParams, tail } from '$lib/format';
  import { weightsName } from '$lib/catalog';
  import type { MemoryPlan, ParamState } from '$proto/estimate_pb';
  import { FitVerdict } from '$proto/estimate_pb';
  import { Play, ArrowLeftRight, ChevronRight } from '@lucide/svelte';
  import Dialog from './ui/Dialog.svelte';
  import Button from './ui/Button.svelte';
  import Checkbox from './ui/Checkbox.svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Choices from './ui/Choices.svelte';
  import Segmented from './ui/Segmented.svelte';
  import Spinner from './ui/Spinner.svelte';
  import Skeleton from './ui/Skeleton.svelte';
  import TextInput from './ui/TextInput.svelte';
  import PlanView from './PlanView.svelte';
  import ParamForm from './ParamForm.svelte';

  // The one place a model is started or swapped in, opened from the library, a slot, or a model's panel
  let pickedKey = $state('');
  let runtimeId = $state('');
  let installId = $state('');
  let name = $state('');
  let slot = $state('');
  let values = $state<Record<string, string>>({});
  let invalid = $state(0);
  let swapMode = $state('overlap');
  let force = $state(false);
  let showParams = $state(true);
  let plan = $state<MemoryPlan | null>(null);
  let states = $state<ParamState[]>([]);
  let planRefusal = $state('');
  let planError = $state('');
  let refusal = $state<unknown>(null);
  let checking = $state(false);
  let generation = 0;

  const model = $derived(runUi.model);
  const stored = $derived([...live.models.values()].sort(byName((m) => m.repo + m.group)));
  const current = $derived(model ?? (pickedKey ? live.models.get(pickedKey) : undefined) ?? null);
  const slots = $derived(orderedSlots());
  const selectedSlot = $derived(slot ? live.slots.get(slot) : undefined);
  const swap = $derived(slotOccupied(slot));
  const formatId = $derived(current?.formatId ?? '');
  const compatible = $derived(cached.runtimes.filter((r) => r.compatible && (!formatId || r.runtime?.formats.includes(formatId))));
  const others = $derived(cached.runtimes.filter((r) => !compatible.includes(r)));
  // The named runtime, else the slot's, else the first that serves the format
  const effectiveRuntime = $derived(runtimeId || selectedSlot?.runtimeId || compatible[0]?.runtime?.id || '');
  const runtime = $derived(cached.runtimes.find((r) => r.runtime?.id === effectiveRuntime)?.runtime);
  const inherited = $derived(selectedSlot?.params ?? {});
  const installs = $derived([...live.installs.values()].filter((i) => i.runtimeId === effectiveRuntime));
  const setCount = $derived(Object.keys(values).length);
  // A plan saying no, or the daemon refusing for it, is what earns the forced launch
  const refused = $derived(plan?.verdict === FitVerdict.NO || !!planRefusal || (code(refusal) === Code.InvalidArgument && message(refusal).includes('pass force')));

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
      force
    };
  }

  // Plans the fit against free memory, dropping an answer the inputs have moved past
  async function check() {
    if (!current) return;
    const gen = ++generation;
    checking = true;
    try {
      const s = spec();
      const resp = await api.estimate.estimate({ sourceId: s.sourceId, repo: s.repo, group: s.group, runtimeId: effectiveRuntime, params: s.params, slotId: s.slotId, free: true });
      if (gen !== generation) return;
      plan = resp.plan ?? null;
      states = resp.params;
      planRefusal = resp.refusal;
    } catch (err) {
      if (gen === generation) planError = message(err);
    } finally {
      if (gen === generation) checking = false;
    }
  }

  // The plan is for one set of inputs, so any change drops it and plans again once typing settles, and
  // the host is read again every ten seconds while the dialog stays open so the bars follow what is running
  const replanEvery = 10_000;
  $effect(() => {
    void runtimeId;
    void slot;
    void values;
    void pickedKey;
    const ready = runUi.open && !!current && !!effectiveRuntime;
    generation++;
    plan = null;
    states = [];
    planRefusal = '';
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
    if (setCount > 0) showParams = true;
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
      force = false;
      showParams = true;
    },
    // The launch toasts its own refusal, the throw only keeps the dialog open
    async submit() {
      const id = await launch(spec(), swapMode === 'drain', (err) => {
        refusal = err;
        planError = message(err);
      });
      if (id === undefined) throw refusal;
    }
  });

  const disabled = $derived(!current || !effectiveRuntime || !installs.length || invalid > 0 || (refused && !force));
</script>

<Dialog bind:open={runUi.open} size="lg" title={swap ? `Swap into ${selectedSlot?.name}` : 'Run a model'} description={current ? `${current.repo} · ${weightsName(current.group, current.formatId)}` : undefined}>
  <form
    class="run-form flex min-w-0 flex-col gap-6"
    onsubmit={(e) => {
      e.preventDefault();
      if (!disabled && !form.saving) form.run();
    }}
  >
    {#if !model}
      <Field label="Model" for="run-model">
        <Select id="run-model" mono bind:value={pickedKey} disabled={!stored.length} empty="No models downloaded" items={stored.map((m) => ({ value: modelKey(m), label: m.repo, detail: weightsName(m.group, m.formatId) }))} />
      </Field>
    {/if}

    {#if current}
      <section class="flex min-w-0 flex-col gap-4" aria-labelledby="run-setup-title">
        <div class="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
          <h2 id="run-setup-title" class="text-sm font-semibold text-fg">Run configuration</h2>
          <details class="model-details text-xs text-fg-muted">
            <summary class="cursor-pointer rounded-sm transition-colors hover:text-fg">{fmtParams(current.descriptor?.parameterCount)} parameters · {bytes(current.bytes)}</summary>
            <dl class="mt-2 flex flex-wrap gap-x-4 gap-y-1">
              <div class="flex gap-1.5"><dt>Format</dt><dd class="font-mono text-fg">{current.formatId}</dd></div>
              {#if current.descriptor?.architecture}<div class="flex gap-1.5"><dt>Architecture</dt><dd class="font-mono text-fg wrap-anywhere">{current.descriptor.architecture}</dd></div>{/if}
              {#if current.descriptor?.bitsPerWeight}<div class="flex gap-1.5"><dt>Bits/weight</dt><dd class="font-mono text-fg">{current.descriptor.bitsPerWeight.toFixed(1)}</dd></div>{/if}
            </dl>
          </details>
        </div>
        {#if slots.length}
          <Field label="Run in" for="run-target">
            <Choices
              label="Run in"
              bind:value={slot}
              items={[
                { id: '', label: 'No slot', detail: 'runs under its own name' },
                ...slots.map((s) => ({ id: s.id, label: `${s.position}. ${s.name}`, mono: true, detail: slotOccupied(s.id) ? `replaces ${tail(s.request?.repo ?? '')}` : 'empty', warn: slotOccupied(s.id) }))
              ]}
            />
          </Field>
        {/if}

        <div class="setup-grid">
          <Field label="Runtime" for="run-runtime" error={!compatible.length ? `No compatible runtime serves ${formatId}` : undefined}>
            <Select
              id="run-runtime"
              bind:value={runtimeId}
              items={[
                { value: '', label: selectedSlot?.runtimeId ? runtimeName(selectedSlot.runtimeId) : (compatible[0]?.runtime?.name ?? 'none'), detail: selectedSlot?.runtimeId ? 'the slot’s runtime' : undefined },
                ...compatible.map((rt) => ({ value: rt.runtime?.id ?? '', label: rt.runtime?.name ?? rt.runtime?.id ?? '' })),
                ...others.map((rt) => ({ value: rt.runtime?.id ?? '', label: rt.runtime?.name ?? rt.runtime?.id ?? '', detail: rt.compatible ? `does not read ${formatId}` : 'not compatible with this host', disabled: true }))
              ]}
            />
          </Field>
          <Field label="Install" for="run-install" error={effectiveRuntime && !installs.length ? `${effectiveRuntime} is not installed` : undefined}>
            <Select id="run-install" bind:value={installId} disabled={!installs.length} items={[{ value: '', label: installs.length ? installs[0].version || installs[0].id : 'None', detail: installs.length ? 'newest' : undefined }, ...installs.map((i) => ({ value: i.id, label: i.version || i.id, detail: tail(i.path) }))]} />
          </Field>
          {#if !slot}
            <Field label="Model name" for="run-name" class="col-span-full">
              <TextInput id="run-name" mono bind:value={name} empty="{tail(current.repo)}:{current.group}" />
            </Field>
          {/if}
        </div>
      </section>

      {#if swap}
        <section class="border-t border-line pt-4" aria-labelledby="run-swap-title">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <h2 id="run-swap-title" class="text-sm font-semibold text-fg">Swap behavior</h2>
            <Segmented bind:value={swapMode} tabs={[{ id: 'overlap', label: 'Side by side' }, { id: 'drain', label: 'Stop first' }]} />
          </div>
          <p class="mt-2 text-xs leading-5 text-fg-muted">{swapMode === 'overlap' ? 'The new model starts beside the old one, then the route flips. Both must fit.' : 'The old model drains and stops before the new one starts. Rolls back on failure.'}</p>
        </section>
      {/if}

      <section class="rounded-lg border border-line bg-sunken/40 p-4" aria-labelledby="run-memory-title" aria-busy={checking}>
        <div class="mb-3 flex flex-wrap items-center gap-2">
          <h2 id="run-memory-title" class="text-sm font-semibold text-fg">Memory estimate</h2>
          {#if selectedSlot}<span class="text-xs text-fg-muted">in {selectedSlot.name}</span>{/if}
          <span class="ml-auto flex items-center gap-1.5 text-xs">
            {#if checking}
              <Spinner size={13} class="text-fg-muted" /><span class="text-fg-muted">Estimating…</span>
            {/if}
          </span>
        </div>
        {#if plan}
          <PlanView {plan} compact />
        {:else if checking}
          <Skeleton rows={3} />
        {:else if !planError}
          <p class="text-xs leading-5 text-fg-muted">{effectiveRuntime ? 'No memory estimate is available.' : 'Choose a runtime to estimate memory.'}</p>
        {/if}
        {#if planError}<div class="note note-bad mt-3">{planError}</div>{/if}
        {#if planRefusal}<div class="note note-warn mt-3">{planRefusal}</div>{/if}
        {#if plan?.detail && plan.verdict !== FitVerdict.FITS}<p class="mt-3 text-xs leading-5 text-fg-muted">{plan.detail}</p>{/if}
        {#if refused || force}
          <div class="mt-4 border-t border-line pt-3"><Checkbox bind:checked={force} label="Run anyway" hint="Launch even though the plan says it will not fit." /></div>
        {/if}
      </section>

      <section class="border-t border-line pt-4" aria-labelledby="run-params-title">
        <h2 id="run-params-title">
          <button type="button" class="flex w-full items-center gap-2 rounded-sm text-left text-sm text-fg" aria-expanded={showParams} aria-controls="run-parameters" onclick={() => (showParams = !showParams)}>
            <ChevronRight size={14} class="shrink-0 text-fg-muted transition-transform {showParams ? 'rotate-90' : ''}" />
            <span class="font-semibold">Parameters</span>
            {#if setCount}<span class="ml-auto text-xs font-normal text-fg-muted">{setCount} overridden</span>{/if}
          </button>
        </h2>
        <div id="run-parameters" class="mt-3" hidden={!showParams}>
          <ParamForm params={runtime?.params ?? []} bind:values bind:invalid {inherited} {states} idPrefix="run" />
        </div>
      </section>
    {/if}
    <button type="submit" class="hidden" aria-hidden="true" tabindex="-1"></button>
  </form>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (runUi.open = false)}>Cancel</Button>
    <span class="ml-auto"><Button variant="primary" icon={swap ? ArrowLeftRight : Play} loading={form.saving} {disabled} onclick={form.run}>{swap ? 'Swap' : 'Run'}</Button></span>
  {/snippet}
</Dialog>

<style>
  .run-form {
    container-type: inline-size;
  }

  .setup-grid {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 1rem;
  }

  .model-details[open] {
    flex-basis: 100%;
  }

  @container (min-width: 30rem) {
    .setup-grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
      column-gap: 1.25rem;
    }
  }
</style>
