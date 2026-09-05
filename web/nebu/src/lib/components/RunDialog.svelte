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
  import { Play, ArrowLeftRight, Gauge } from '@lucide/svelte';
  import FormDialog from './ui/FormDialog.svelte';
  import CheckCard from './ui/CheckCard.svelte';
  import Field from './ui/Field.svelte';
  import Button from './ui/Button.svelte';
  import PlanView from './PlanView.svelte';
  import SwapTarget from './SwapTarget.svelte';

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
  let drainFirst = $state(false);
  let force = $state(false);
  let plan = $state<MemoryPlan | null>(null);
  let planError = $state('');
  let refusal = $state<unknown>(null);
  let checking = $state(false);

  const stored = $derived([...live.models.values()].sort(byName((m) => m.repo + m.group)));
  const current = $derived(model ?? (pickedKey ? live.models.get(pickedKey) : undefined) ?? null);
  const selectedSlot = $derived(slot ? live.slots.get(slot) : undefined);
  const swap = $derived(slotOccupied(slot));
  const installs = $derived([...live.installs.values()].filter((i) => i.runtimeId === effectiveRuntime));
  // Only a plan saying no, or a refusal for it, earns the forced launch
  const refused = $derived(plan?.verdict === FitVerdict.NO || (refusal instanceof ConnectError && refusal.code === Code.InvalidArgument && refusal.rawMessage.includes('pass force')));

  // A plan is for one set of inputs, so any change invalidates it
  $effect(() => {
    void runtimeId;
    void slot;
    void profileId;
    void values;
    void pickedKey;
    plan = null;
    planError = '';
    refusal = null;
  });

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

  async function check() {
    if (!current) return;
    checking = true;
    planError = '';
    try {
      const s = spec();
      const resp = await api.estimate.estimate({ sourceId: s.sourceId, repo: s.repo, group: s.group, runtimeId: effectiveRuntime, params: s.params, slotId: s.slotId, profileId: s.profileId, free: true });
      plan = resp.plan ?? null;
    } catch (err) {
      planError = message(err);
    } finally {
      checking = false;
    }
  }

  const form = createForm({
    open: () => open,
    close: () => (open = false),
    reset() {
      slot = slotId;
      pickedKey = model ? modelKey(model) : stored[0] ? modelKey(stored[0]) : '';
      runtimeId = installId = name = profileId = '';
      values = {};
      drainFirst = force = false;
      plan = null;
      planError = '';
      refusal = null;
    },
    // The launch toasts its own refusal, the throw only keeps the dialog open
    async submit() {
      const id = await launch(spec(), drainFirst, (err) => {
        refusal = err;
        planError = message(err);
      });
      if (id === undefined) throw refusal;
    }
  });
</script>

<FormDialog
  bind:open
  title={swap ? 'Swap model' : 'Run model'}
  description={current ? `${current.repo} · ${current.group}` : undefined}
  size="lg"
  action={swap ? 'Swap' : 'Run'}
  icon={swap ? ArrowLeftRight : Play}
  saving={form.saving}
  disabled={!current || !effectiveRuntime || invalid > 0}
  onsubmit={form.run}
>
  {#if !model}
    <Field label="Model" for="run-model" class="mb-4" hint={stored.length ? '' : 'Nothing stored'}>
      <select id="run-model" class="input font-mono" bind:value={pickedKey}>
        {#each stored as m (modelKey(m))}
          <option value={modelKey(m)}>{m.repo} · {m.group}</option>
        {/each}
      </select>
    </Field>
  {/if}

  {#if current}
    <div class="mb-4 flex flex-wrap gap-x-5 gap-y-1 rounded-lg border border-line bg-sunken px-3 py-2 text-xs text-fg-muted">
      <span>format <span class="text-fg">{current.formatId}</span></span>
      <span>arch <span class="text-fg">{current.descriptor?.architecture || '–'}</span></span>
      <span>params <span class="text-fg">{fmtParams(current.descriptor?.parameterCount)}</span></span>
      <span>weights <span class="text-fg">{bytes(current.bytes)}</span></span>
      {#if current.descriptor?.bitsPerWeight}<span>bpw <span class="text-fg">{current.descriptor.bitsPerWeight.toFixed(2)}</span></span>{/if}
    </div>

    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
      <SwapTarget bind:slotId={slot} bind:runtimeId bind:profileId bind:values bind:invalid bind:effectiveRuntime formatId={current.formatId} idPrefix="run">
        {#if !slot}
          <Field label="Name" for="run-name" hint="The model name at the gateway">
            <input id="run-name" class="input font-mono" bind:value={name} placeholder="{current.repo.split('/').pop()}:{current.group}" />
          </Field>
        {:else}
          <Field label="Name">
            <div class="input flex items-center font-mono text-fg-muted">{selectedSlot?.name}</div>
          </Field>
        {/if}
        <Field label="Install" for="run-install" hint={installs.length || !effectiveRuntime ? '' : `No install of ${effectiveRuntime}`}>
          <select id="run-install" class="input" bind:value={installId}>
            <option value="">Newest</option>
            {#each installs as i (i.id)}
              <option value={i.id}>{i.version || i.id} · {i.path}</option>
            {/each}
          </select>
        </Field>
      </SwapTarget>
      {#if swap}
        <CheckCard bind:checked={drainFirst} class="sm:col-span-2" title="Stop the current model first" description="Otherwise the new one starts beside it when memory allows" />
      {/if}
    </div>

    <div class="mt-4 rounded-lg border border-line p-3">
      <div class="flex items-center gap-2">
        <Gauge size={14} class="text-fg-muted" />
        <span class="text-sm font-medium text-fg">Fit</span>
        <span class="text-xs text-fg-faint">free memory{selectedSlot ? ` in ${selectedSlot.name}` : ''}</span>
        <Button size="sm" variant="outline" class="ml-auto" loading={checking} onclick={check} disabled={!effectiveRuntime}>Check</Button>
      </div>
      {#if plan}
        <div class="mt-3"><PlanView {plan} /></div>
      {:else if planError}
        <div class="mt-2 text-sm text-bad">{planError}</div>
      {:else}
        <p class="mt-2 text-xs text-fg-faint">A run that does not fit is refused unless forced</p>
      {/if}
      {#if refused || force}
        <CheckCard bind:checked={force} class="mt-3" title="Run anyway" description="The runtime may still refuse or spill to host memory" />
      {/if}
    </div>
  {/if}
</FormDialog>
