<script lang="ts">
  import { api, message } from '$lib/api';
  import { live, instanceLive, modelKey } from '$lib/state.svelte';
  import { launch, slotOccupied } from '$lib/launch';
  import { parsePairs, bytes, params as fmtParams } from '$lib/format';
  import type { StoredModel } from '$proto/store_pb';
  import type { MemoryPlan } from '$proto/estimate_pb';
  import type { RuntimeStatus } from '$proto/runtime_pb';
  import { Play, ArrowLeftRight, Gauge } from '@lucide/svelte';
  import Dialog from './ui/Dialog.svelte';
  import Field from './ui/Field.svelte';
  import Button from './ui/Button.svelte';
  import PlanView from './PlanView.svelte';

  let { open = $bindable(false), model = null, slotId = '' }: { open?: boolean; model?: StoredModel | null; slotId?: string } = $props();

  let runtimes = $state<RuntimeStatus[]>([]);
  let pickedKey = $state('');
  let runtimeId = $state('');
  let installId = $state('');
  let name = $state('');
  let slot = $state('');
  let paramsText = $state('');
  let drainFirst = $state(false);
  let plan = $state<MemoryPlan | null>(null);
  let planError = $state('');
  let checking = $state(false);
  let submitting = $state(false);

  const stored = $derived([...live.models.values()].sort((a, b) => (a.repo + a.group).localeCompare(b.repo + b.group)));
  const current = $derived(model ?? (pickedKey ? live.models.get(pickedKey) : undefined) ?? null);
  const selectedSlot = $derived(slot ? live.slots.get(slot) : undefined);
  const swap = $derived(slotOccupied(slot));
  const compatible = $derived(runtimes.filter((r) => r.compatible && r.manifest?.formats.includes(current?.formatId ?? '')));
  const others = $derived(runtimes.filter((r) => !compatible.includes(r)));
  const effectiveRuntime = $derived(runtimeId || selectedSlot?.runtimeId || compatible[0]?.manifest?.id || '');
  const installs = $derived([...live.installs.values()].filter((i) => i.runtimeId === effectiveRuntime));

  $effect(() => {
    if (!open) return;
    slot = slotId;
    pickedKey = model ? modelKey(model) : (stored[0] ? modelKey(stored[0]) : '');
    runtimeId = '';
    installId = '';
    name = '';
    paramsText = '';
    drainFirst = false;
    plan = null;
    planError = '';
    api.runtimes.listRuntimes({}).then((r) => (runtimes = r.runtimes)).catch(() => (runtimes = []));
  });

  // A plan is for one set of inputs, so any change invalidates it
  $effect(() => {
    void runtimeId;
    void slot;
    void paramsText;
    void pickedKey;
    plan = null;
    planError = '';
  });

  function spec() {
    return {
      sourceId: current!.sourceId,
      repo: current!.repo,
      group: current!.group,
      runtimeId,
      installId,
      name: slot ? '' : name,
      params: parsePairs(paramsText),
      slotId: slot
    };
  }

  async function check() {
    if (!current) return;
    checking = true;
    planError = '';
    try {
      const s = spec();
      const resp = await api.estimate.estimate({ sourceId: s.sourceId, repo: s.repo, group: s.group, runtimeId: effectiveRuntime, params: s.params, slotId: s.slotId });
      plan = resp.plan ?? null;
    } catch (err) {
      planError = message(err);
    } finally {
      checking = false;
    }
  }

  async function submit() {
    if (!current) return;
    submitting = true;
    const id = await launch(spec(), drainFirst);
    submitting = false;
    if (id !== undefined) open = false;
  }
</script>

<Dialog bind:open title={swap ? 'Swap model' : 'Run model'} description={current ? `${current.repo} · ${current.group}` : 'Pick a stored model'} size="lg">
  {#if !model}
    <Field label="Stored model" for="run-model" class="mb-4" hint={stored.length ? '' : 'Nothing is stored yet, pull a model from the catalog first'}>
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
      <Field label="Slot" for="run-slot" hint={swap ? 'Occupied, so the run becomes a swap' : 'A slot pins devices, budget, and public name'}>
        <select id="run-slot" class="input" bind:value={slot}>
          <option value="">No slot, run on the whole host</option>
          {#each [...live.slots.values()] as s (s.id)}
            {@const busy = !!s.instanceId && instanceLive(live.instances.get(s.instanceId))}
            <option value={s.id}>{s.name}{busy ? ` · serving ${s.request?.repo ?? ''}` : ' · empty'}</option>
          {/each}
        </select>
      </Field>

      {#if !slot}
        <Field label="Public name" for="run-name" hint="How the gateway addresses it, defaults to repo:group">
          <input id="run-name" class="input font-mono" bind:value={name} placeholder="{current.repo.split('/').pop()}:{current.group}" />
        </Field>
      {:else}
        <Field label="Public name" hint="Slots own their name">
          <div class="input flex items-center font-mono text-fg-muted">{selectedSlot?.name}</div>
        </Field>
      {/if}

      <Field label="Runtime" for="run-runtime" hint={compatible.length ? '' : 'No compatible runtime accepts this format on this host'}>
        <select id="run-runtime" class="input" bind:value={runtimeId}>
          <option value="">{selectedSlot?.runtimeId ? `Slot default · ${selectedSlot.runtimeId}` : compatible[0] ? `Auto · ${compatible[0].manifest?.id}` : 'Auto'}</option>
          {#each compatible as rt (rt.manifest?.id)}
            <option value={rt.manifest?.id}>{rt.manifest?.name ?? rt.manifest?.id}</option>
          {/each}
          {#each others as rt (rt.manifest?.id)}
            <option value={rt.manifest?.id} disabled>{rt.manifest?.name ?? rt.manifest?.id} · {rt.compatible ? 'format not accepted' : 'incompatible host'}</option>
          {/each}
        </select>
      </Field>

      <Field label="Install" for="run-install" hint={installs.length || !effectiveRuntime ? '' : `No install for ${effectiveRuntime} yet, adopt or build one`}>
        <select id="run-install" class="input" bind:value={installId}>
          <option value="">Newest install</option>
          {#each installs as i (i.id)}
            <option value={i.id}>{i.version || i.id} · {i.path}</option>
          {/each}
        </select>
      </Field>

      <Field label="Parameters" for="run-params" hint="One name=value per line. Slot defaults apply first, and auto values are solved by the planner" class="sm:col-span-2">
        <textarea id="run-params" class="input h-24" bind:value={paramsText} placeholder="n_ctx=8192&#10;n_gpu_layers=auto"></textarea>
      </Field>

      {#if swap}
        <label class="flex cursor-pointer items-start gap-3 rounded-lg border border-line bg-sunken px-3 py-2.5 sm:col-span-2">
          <input type="checkbox" class="mt-0.5 accent-accent" bind:checked={drainFirst} />
          <span class="text-sm">
            <span class="font-medium text-fg">Drain first</span>
            <span class="block text-xs leading-5 text-fg-muted">Stop the current model before starting the new one. Otherwise the new one starts beside it and the name flips once it is healthy, when memory allows.</span>
          </span>
        </label>
      {/if}
    </div>

    <div class="mt-4 rounded-lg border border-line p-3">
      <div class="flex items-center gap-2">
        <Gauge size={14} class="text-fg-muted" />
        <span class="text-sm font-medium text-fg">Memory plan</span>
        <span class="text-xs text-fg-faint">against free memory{selectedSlot ? ` inside ${selectedSlot.name}` : ''}</span>
        <Button size="sm" variant="outline" class="ml-auto" loading={checking} onclick={check} disabled={!effectiveRuntime}>Check fit</Button>
      </div>
      {#if plan}
        <div class="mt-3"><PlanView {plan} /></div>
      {:else if planError}
        <div class="mt-2 text-sm text-bad">{planError}</div>
      {:else}
        <p class="mt-2 text-xs text-fg-faint">Check before launching to see where weights and cache land. A run is refused when the plan says no.</p>
      {/if}
    </div>
  {/if}

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button variant="primary" icon={swap ? ArrowLeftRight : Play} loading={submitting} onclick={submit} disabled={!current || !effectiveRuntime}>{swap ? 'Swap' : 'Run'}</Button>
  {/snippet}
</Dialog>
