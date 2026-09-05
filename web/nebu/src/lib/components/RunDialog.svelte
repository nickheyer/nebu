<script lang="ts">
  import { Code, ConnectError } from '@connectrpc/connect';
  import { api, message } from '$lib/api';
  import { live, modelKey } from '$lib/state.svelte';
  import { launch, slotOccupied } from '$lib/launch';
  import { createForm } from '$lib/form.svelte';
  import { byName, bytes, params as fmtParams } from '$lib/format';
  import type { StoredModel } from '$proto/store_pb';
  import type { MemoryPlan } from '$proto/estimate_pb';
  import { FitVerdict } from '$proto/estimate_pb';
  import { Play, ArrowLeftRight, Check } from '@lucide/svelte';
  import FormDialog from './ui/FormDialog.svelte';
  import Checkbox from './ui/Checkbox.svelte';
  import Field from './ui/Field.svelte';
  import Segmented from './ui/Segmented.svelte';
  import Spinner from './ui/Spinner.svelte';
  import Skeleton from './ui/Skeleton.svelte';
  import PlanView from './PlanView.svelte';
  import RunTarget from './RunTarget.svelte';

  let { open = $bindable(false), model = null, slotId = '' }: { open?: boolean; model?: StoredModel | null; slotId?: string } = $props();

  let pickedKey = $state('');
  let runtimeId = $state('');
  let installId = $state('');
  let name = $state('');
  let slot = $state('');
  let profileId = $state('');
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

  const stored = $derived([...live.models.values()].sort(byName((m) => m.repo + m.group)));
  const current = $derived(model ?? (pickedKey ? live.models.get(pickedKey) : undefined) ?? null);
  const slots = $derived([...live.slots.values()].sort(byName((s) => s.name)));
  const selectedSlot = $derived(slot ? live.slots.get(slot) : undefined);
  const swap = $derived(slotOccupied(slot));
  const installs = $derived([...live.installs.values()].filter((i) => i.runtimeId === effectiveRuntime));
  // A plan saying no, or the daemon refusing for it, is what earns the forced launch
  const refused = $derived(plan?.verdict === FitVerdict.NO || (refusal instanceof ConnectError && refusal.code === Code.InvalidArgument && refusal.rawMessage.includes('pass force')));

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
      profileId,
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
      const resp = await api.estimate.estimate({ sourceId: s.sourceId, repo: s.repo, group: s.group, runtimeId: effectiveRuntime, params: s.params, slotId: s.slotId, profileId: s.profileId, free: true });
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
    void profileId;
    void values;
    void pickedKey;
    const ready = open && !!current && !!effectiveRuntime;
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
    open: () => open,
    close: () => (open = false),
    reset() {
      slot = slotId;
      pickedKey = model ? modelKey(model) : stored[0] ? modelKey(stored[0]) : '';
      runtimeId = installId = name = profileId = '';
      values = {};
      swapMode = 'overlap';
      force = false;
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

  const choice = 'flex min-h-16 flex-col justify-center rounded-lg border px-3.5 py-2.5 text-left transition-colors';
</script>

<FormDialog
  bind:open
  title={swap ? `Swap into ${selectedSlot?.name}` : 'Run a model'}
  description={current ? `${current.repo} · ${current.group}` : undefined}
  size="lg"
  action={swap ? 'Swap' : 'Run'}
  icon={swap ? ArrowLeftRight : Play}
  saving={form.saving}
  disabled={!current || !effectiveRuntime || invalid > 0 || (refused && !force)}
  onsubmit={form.run}
>
  <div class="flex flex-col gap-6">
    {#if !model}
      <Field label="Model" for="run-model">
        <select id="run-model" class="input font-mono" bind:value={pickedKey} disabled={!stored.length}>
          {#each stored as m (modelKey(m))}
            <option value={modelKey(m)}>{m.repo} · {m.group}</option>
          {:else}
            <option value="">Nothing in the library</option>
          {/each}
        </select>
      </Field>
    {/if}

    {#if current}
      <div class="flex flex-wrap gap-x-5 gap-y-1 text-sm text-fg-muted">
        <span>{current.formatId}</span>
        {#if current.descriptor?.architecture}<span>{current.descriptor.architecture}</span>{/if}
        <span>{fmtParams(current.descriptor?.parameterCount)} params</span>
        <span>{bytes(current.bytes)}</span>
        {#if current.descriptor?.bitsPerWeight}<span>{current.descriptor.bitsPerWeight.toFixed(1)} bits per weight</span>{/if}
      </div>

      <div>
        <div class="mb-2 text-sm font-medium text-fg-muted">Where</div>
        <div role="radiogroup" aria-label="Where" class="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          <button type="button" role="radio" aria-checked={!slot} class="{choice} {!slot ? 'border-accent bg-accent/8' : 'border-line hover:border-line-strong'}" onclick={() => (slot = '')}>
            <span class="flex items-center gap-2 text-sm font-medium text-fg">Standalone {#if !slot}<Check size={14} class="ml-auto text-accent" />{/if}</span>
            <span class="text-xs text-fg-faint">Its own name, no reservation</span>
          </button>
          {#each slots as s (s.id)}
            {@const on = slot === s.id}
            {@const busy = slotOccupied(s.id)}
            <button type="button" role="radio" aria-checked={on} class="{choice} {on ? 'border-accent bg-accent/8' : 'border-line hover:border-line-strong'}" onclick={() => (slot = s.id)}>
              <span class="flex items-center gap-2 font-mono text-sm font-medium text-fg">{s.name} {#if on}<Check size={14} class="ml-auto text-accent" />{/if}</span>
              <span class="truncate text-xs {busy ? 'text-warn' : 'text-fg-faint'}">{busy ? `Swaps out ${s.request?.repo?.split('/').pop() ?? 'the current model'}` : 'Empty'}</span>
            </button>
          {/each}
        </div>
      </div>

      <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <RunTarget bind:slotId={slot} bind:runtimeId bind:profileId bind:values bind:invalid bind:effectiveRuntime formatId={current.formatId} slotPicker={false} idPrefix="run">
          <Field label="Install" for="run-install" error={effectiveRuntime && !installs.length ? `No install of ${effectiveRuntime}` : undefined}>
            <select id="run-install" class="input" bind:value={installId} disabled={!installs.length}>
              <option value="">{installs.length ? 'Newest' : effectiveRuntime ? 'None' : '–'}</option>
              {#each installs as i (i.id)}
                <option value={i.id}>{i.version || i.id} · {i.path}</option>
              {/each}
            </select>
          </Field>
          {#if !slot}
            <Field label="Model name" for="run-name" info="What clients send as the model" class="sm:col-span-2">
              <input id="run-name" class="input font-mono" bind:value={name} placeholder="{current.repo.split('/').pop()}:{current.group}" autocomplete="off" spellcheck="false" />
            </Field>
          {/if}
        </RunTarget>
      </div>

      {#if swap}
        <div>
          <div class="mb-2 text-sm font-medium text-fg-muted">How to swap</div>
          <Segmented bind:value={swapMode} tabs={[{ id: 'overlap', label: 'Overlap' }, { id: 'drain', label: 'Drain first' }]} />
          <p class="mt-1.5 text-xs text-fg-faint">{swapMode === 'overlap' ? 'Starts the new model beside the current one and stops the old once the new answers. Needs room for both.' : 'Stops the current model first, then starts the new one in its place.'}</p>
        </div>
      {/if}

      <div class="rounded-lg border border-line bg-bg/40 p-4">
        <div class="flex items-center gap-2">
          <span class="text-sm font-medium text-fg">Memory</span>
          <span class="text-xs text-fg-faint">{selectedSlot ? `in ${selectedSlot.name}` : 'against what is free now'}</span>
          {#if checking}<Spinner size={13} class="ml-auto text-fg-faint" />{/if}
        </div>
        {#if plan}
          <div class="mt-3"><PlanView {plan} compact /></div>
        {:else if planError}
          <div class="note note-bad mt-3">{planError}</div>
        {:else if checking}
          <div class="mt-3"><Skeleton rows={2} /></div>
        {/if}
        {#if refused || force}
          <Checkbox bind:checked={force} class="mt-3" title="Run anyway" hint="The plan says it will not fit. The runtime may still manage, or fail" />
        {/if}
      </div>
    {/if}
  </div>
</FormDialog>
