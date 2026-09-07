<script lang="ts">
  import { Code } from '@connectrpc/connect';
  import { api, code, message } from '$lib/api';
  import { live, modelKey } from '$lib/state.svelte';
  import { runUi } from '$lib/slotActions.svelte';
  import { launch, slotOccupied } from '$lib/launch';
  import { createForm } from '$lib/form.svelte';
  import { byName, bytes, params as fmtParams, tail } from '$lib/format';
  import { weightsName } from '$lib/catalog';
  import type { MemoryPlan } from '$proto/estimate_pb';
  import { FitVerdict } from '$proto/estimate_pb';
  import { Play, ArrowLeftRight } from '@lucide/svelte';
  import Drawer from './ui/Drawer.svelte';
  import Button from './ui/Button.svelte';
  import Checkbox from './ui/Checkbox.svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Choices from './ui/Choices.svelte';
  import Segmented from './ui/Segmented.svelte';
  import Spinner from './ui/Spinner.svelte';
  import Skeleton from './ui/Skeleton.svelte';
  import Section from './ui/Section.svelte';
  import PlanView from './PlanView.svelte';
  import RunTarget from './RunTarget.svelte';

  // The one place a model is started or swapped in, opened from the library, a slot, or a model's panel
  let pickedKey = $state('');
  let runtimeId = $state('');
  let installId = $state('');
  let name = $state('');
  let slot = $state('');
  let values = $state<Record<string, string>>({});
  let invalid = $state(0);
  let effectiveRuntime = $state('');
  let swapMode = $state('overlap');
  let force = $state(false);
  let plan = $state<MemoryPlan | null>(null);
  let planError = $state('');
  let refusal = $state<unknown>(null);
  let checking = $state(false);
  let generation = 0;

  const model = $derived(runUi.model);
  const stored = $derived([...live.models.values()].sort(byName((m) => m.repo + m.group)));
  const current = $derived(model ?? (pickedKey ? live.models.get(pickedKey) : undefined) ?? null);
  const slots = $derived([...live.slots.values()].sort(byName((s) => s.name)));
  const selectedSlot = $derived(slot ? live.slots.get(slot) : undefined);
  const swap = $derived(slotOccupied(slot));
  const installs = $derived([...live.installs.values()].filter((i) => i.runtimeId === effectiveRuntime));
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
    },
    // The launch toasts its own refusal, the throw only keeps the panel open
    async submit() {
      const id = await launch(spec(), swapMode === 'drain', (err) => {
        refusal = err;
        planError = message(err);
      });
      if (id === undefined) throw refusal;
    }
  });

  const disabled = $derived(!current || !effectiveRuntime || invalid > 0 || (refused && !force));
</script>

<Drawer bind:open={runUi.open} title={swap ? `Swap into ${selectedSlot?.name}` : 'Run a model'} subtitle={current ? `${current.repo} · ${weightsName(current.group, current.formatId)}` : undefined}>
  <form
    class="flex flex-col gap-6 px-6 py-5"
    onsubmit={(e) => {
      e.preventDefault();
      if (!disabled && !form.saving) form.run();
    }}
  >
    {#if !model}
      <Field label="Model" for="run-model">
        <Select id="run-model" mono bind:value={pickedKey} disabled={!stored.length} placeholder="Nothing in the library" items={stored.map((m) => ({ value: modelKey(m), label: m.repo, detail: weightsName(m.group, m.formatId) }))} />
      </Field>
    {/if}

    {#if current}
      <div class="flex flex-wrap gap-x-4 gap-y-1 text-sm text-fg-muted">
        <span>{current.formatId}</span>
        {#if current.descriptor?.architecture}<span>{current.descriptor.architecture}</span>{/if}
        <span>{fmtParams(current.descriptor?.parameterCount)} params</span>
        <span>{bytes(current.bytes)}</span>
        {#if current.descriptor?.bitsPerWeight}<span>{current.descriptor.bitsPerWeight.toFixed(1)} bits per weight</span>{/if}
      </div>

      <Section title="Where">
        <Choices
          label="Where"
          bind:value={slot}
          items={[
            { id: '', label: 'Standalone', detail: 'its own name' },
            ...slots.map((s) => ({ id: s.id, label: s.name, mono: true, detail: slotOccupied(s.id) ? `swaps out ${tail(s.request?.repo ?? '')}` : 'empty', warn: slotOccupied(s.id) }))
          ]}
        />
      </Section>

      <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <RunTarget bind:slotId={slot} bind:runtimeId bind:values bind:invalid bind:effectiveRuntime formatId={current.formatId} slotPicker={false} idPrefix="run">
          <Field label="Install" for="run-install" error={effectiveRuntime && !installs.length ? `No install of ${effectiveRuntime}` : undefined}>
            <Select id="run-install" bind:value={installId} disabled={!installs.length} items={[{ value: '', label: installs.length ? 'Newest' : effectiveRuntime ? 'None' : '–' }, ...installs.map((i) => ({ value: i.id, label: i.version || i.id, detail: tail(i.path) }))]} />
          </Field>
          {#if !slot}
            <Field label="Model name" for="run-name" hint="What clients send as the model" class="sm:col-span-2">
              <input id="run-name" class="input font-mono" bind:value={name} placeholder="{tail(current.repo)}:{current.group}" autocomplete="off" spellcheck="false" />
            </Field>
          {/if}
        </RunTarget>
      </div>

      {#if swap}
        <Section title="Swap">
          <Segmented bind:value={swapMode} tabs={[{ id: 'overlap', label: 'Overlap' }, { id: 'drain', label: 'Drain first' }]} />
          <p class="text-xs text-fg-faint">{swapMode === 'overlap' ? 'The new model starts beside the old one, so both must fit' : 'The old model stops before the new one starts'}</p>
        </Section>
      {/if}

      <Section title="Memory" meta={selectedSlot ? `in ${selectedSlot.name}` : 'free now'}>
        {#snippet actions()}
          {#if checking}<Spinner size={13} class="text-fg-faint" />{/if}
        {/snippet}
        {#if plan}
          <PlanView {plan} compact />
        {:else if planError}
          <div class="note note-bad">{planError}</div>
        {:else if checking}
          <Skeleton rows={2} />
        {/if}
        {#if refused || force}
          <Checkbox bind:checked={force} label="Run anyway" hint="The plan says it will not fit" />
        {/if}
      </Section>
    {/if}
    <button type="submit" class="hidden" aria-hidden="true" tabindex="-1"></button>
  </form>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (runUi.open = false)}>Cancel</Button>
    <span class="ml-auto"><Button variant="primary" icon={swap ? ArrowLeftRight : Play} loading={form.saving} {disabled} onclick={form.run}>{swap ? 'Swap' : 'Run'}</Button></span>
  {/snippet}
</Drawer>
