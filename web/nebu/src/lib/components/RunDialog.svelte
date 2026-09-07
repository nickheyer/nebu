<script lang="ts">
  import { Code } from '@connectrpc/connect';
  import { api, code, message } from '$lib/api';
  import { live, cached, modelKey, orderedSlots } from '$lib/state.svelte';
  import { runUi } from '$lib/actions.svelte';
  import { launch, slotOccupied } from '$lib/launch';
  import { createForm } from '$lib/form.svelte';
  import { byName, bytes, params as fmtParams, tail } from '$lib/format';
  import { weightsName } from '$lib/catalog';
  import type { MemoryPlan } from '$proto/estimate_pb';
  import { FitVerdict } from '$proto/estimate_pb';
  import { Play, ArrowLeftRight } from '@lucide/svelte';
  import Dialog from './ui/Dialog.svelte';
  import Button from './ui/Button.svelte';
  import Checkbox from './ui/Checkbox.svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Choices from './ui/Choices.svelte';
  import Segmented from './ui/Segmented.svelte';
  import Spinner from './ui/Spinner.svelte';
  import Skeleton from './ui/Skeleton.svelte';
  import Disclosure from './ui/Disclosure.svelte';
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
  let showParams = $state(false);
  let plan = $state<MemoryPlan | null>(null);
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
  const compatible = $derived(cached.runtimes.filter((r) => r.compatible && (!formatId || r.manifest?.formats.includes(formatId))));
  const others = $derived(cached.runtimes.filter((r) => !compatible.includes(r)));
  // The named runtime, else the slot's, else the first that serves the format
  const effectiveRuntime = $derived(runtimeId || selectedSlot?.runtimeId || compatible[0]?.manifest?.id || '');
  const manifest = $derived(cached.runtimes.find((r) => r.manifest?.id === effectiveRuntime)?.manifest);
  const inherited = $derived(selectedSlot?.params ?? {});
  const installs = $derived([...live.installs.values()].filter((i) => i.runtimeId === effectiveRuntime));
  const setCount = $derived(Object.keys(values).length);
  // A plan saying no, or the daemon refusing for it, is what earns the forced launch
  const refused = $derived(plan?.verdict === FitVerdict.NO || (code(refusal) === Code.InvalidArgument && message(refusal).includes('pass force')));

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
      if (gen === generation) plan = resp.plan ?? null;
    } catch (err) {
      if (gen === generation) planError = message(err);
    } finally {
      if (gen === generation) checking = false;
    }
  }

  // The plan is for one set of inputs, so any change drops it and plans again once typing settles
  $effect(() => {
    void runtimeId;
    void slot;
    void values;
    void pickedKey;
    const ready = runUi.open && !!current && !!effectiveRuntime;
    generation++;
    plan = null;
    planError = '';
    refusal = null;
    if (!ready) {
      checking = false;
      return;
    }
    checking = true;
    const timer = setTimeout(() => void check(), 350);
    return () => clearTimeout(timer);
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
      runtimeId = installId = name = '';
      values = {};
      swapMode = 'overlap';
      force = false;
      showParams = false;
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

<Dialog bind:open={runUi.open} size="xl" title={swap ? `Swap into ${selectedSlot?.name}` : 'Run a model'} description={current ? `${current.repo} · ${weightsName(current.group, current.formatId)}` : undefined}>
  <form
    class="grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,1fr)_22rem]"
    onsubmit={(e) => {
      e.preventDefault();
      if (!disabled && !form.saving) form.run();
    }}
  >
    <div class="flex flex-col gap-5">
      {#if !model}
        <Field label="Model" for="run-model">
          <Select id="run-model" mono bind:value={pickedKey} disabled={!stored.length} empty="No models downloaded" items={stored.map((m) => ({ value: modelKey(m), label: m.repo, detail: weightsName(m.group, m.formatId) }))} />
        </Field>
      {/if}

      {#if current}
        <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-fg-muted">
          <span class="kv"><span>format</span><span>{current.formatId}</span></span>
          {#if current.descriptor?.architecture}<span class="kv"><span>arch</span><span>{current.descriptor.architecture}</span></span>{/if}
          <span class="kv"><span>params</span><span>{fmtParams(current.descriptor?.parameterCount)}</span></span>
          <span class="kv"><span>size</span><span>{bytes(current.bytes)}</span></span>
          {#if current.descriptor?.bitsPerWeight}<span class="kv"><span>bits/weight</span><span>{current.descriptor.bitsPerWeight.toFixed(1)}</span></span>{/if}
        </div>

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

        <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
          <Field label="Runtime" for="run-runtime" error={!compatible.length ? `No compatible runtime serves ${formatId}` : undefined}>
            <Select
              id="run-runtime"
              bind:value={runtimeId}
              items={[
                { value: '', label: selectedSlot?.runtimeId ? `Slot default: ${selectedSlot.runtimeId}` : `Default: ${compatible[0]?.manifest?.name ?? 'none'}` },
                ...compatible.map((rt) => ({ value: rt.manifest?.id ?? '', label: rt.manifest?.name ?? rt.manifest?.id ?? '' })),
                ...others.map((rt) => ({ value: rt.manifest?.id ?? '', label: rt.manifest?.name ?? rt.manifest?.id ?? '', detail: rt.compatible ? `does not read ${formatId}` : 'not compatible with this host', disabled: true }))
              ]}
            />
          </Field>
          <Field label="Install" for="run-install" error={effectiveRuntime && !installs.length ? `${effectiveRuntime} is not installed` : undefined}>
            <Select id="run-install" bind:value={installId} disabled={!installs.length} items={[{ value: '', label: installs.length ? `Newest: ${installs[0].version || installs[0].id}` : 'None' }, ...installs.map((i) => ({ value: i.id, label: i.version || i.id, detail: tail(i.path) }))]} />
          </Field>
          {#if !slot}
            <Field label="Model name" for="run-name" description="Clients send this as the model name." class="sm:col-span-2">
              <TextInput id="run-name" mono bind:value={name} empty="{tail(current.repo)}:{current.group}" />
            </Field>
          {/if}
        </div>

        <Disclosure label="Parameters" summary={setCount ? `${setCount} set` : selectedSlot && Object.keys(inherited).length ? 'slot defaults' : 'runtime defaults'} bind:open={showParams}>
          <ParamForm params={manifest?.params ?? []} bind:values bind:invalid {inherited} idPrefix="run" />
        </Disclosure>
      {/if}
    </div>

    <div class="flex flex-col gap-4">
      {#if current}
        <div class="card p-4">
          <div class="mb-3 flex items-center gap-2">
            <h3 class="text-sm font-semibold text-fg">Memory</h3>
            <span class="text-xs text-fg-faint">{selectedSlot ? `in ${selectedSlot.name}` : 'against free memory'}</span>
            {#if checking}<Spinner size={13} class="ml-auto text-fg-faint" />{/if}
          </div>
          {#if plan}
            <PlanView {plan} compact />
          {:else if planError}
            <div class="note note-bad">{planError}</div>
          {:else if checking}
            <Skeleton rows={3} />
          {/if}
          {#if refused || force}
            <div class="mt-3"><Checkbox bind:checked={force} label="Run anyway" hint="Launch even though the plan says it will not fit." /></div>
          {/if}
        </div>
        {#if swap}
          <div class="card p-4">
            <h3 class="mb-3 text-sm font-semibold text-fg">Swap</h3>
            <Segmented bind:value={swapMode} tabs={[{ id: 'overlap', label: 'Side by side' }, { id: 'drain', label: 'Stop first' }]} />
            <p class="mt-2 text-xs leading-5 text-fg-muted">{swapMode === 'overlap' ? 'The new model starts beside the old one, then the route flips. Both must fit.' : 'The old model drains and stops before the new one starts. Rolls back on failure.'}</p>
          </div>
        {/if}
      {/if}
    </div>
    <button type="submit" class="hidden" aria-hidden="true" tabindex="-1"></button>
  </form>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (runUi.open = false)}>Cancel</Button>
    <span class="ml-auto"><Button variant="primary" icon={swap ? ArrowLeftRight : Play} loading={form.saving} {disabled} onclick={form.run}>{swap ? 'Swap' : 'Run'}</Button></span>
  {/snippet}
</Dialog>
