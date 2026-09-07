<script lang="ts">
  import type { Snippet } from 'svelte';
  import { live, cached } from '$lib/state.svelte';
  import { slotOccupied } from '$lib/launch';
  import { byName } from '$lib/format';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Disclosure from './ui/Disclosure.svelte';
  import ParamForm from './ParamForm.svelte';

  // Where and how a model runs: the slot, the runtime it resolves to, and the parameters over the slot's
  let {
    slotId = $bindable(''),
    runtimeId = $bindable(''),
    values = $bindable({}),
    invalid = $bindable(0),
    effectiveRuntime = $bindable(''),
    formatId = '',
    slotPicker = true,
    idPrefix = 'run',
    children
  }: {
    slotId?: string;
    runtimeId?: string;
    values?: Record<string, string>;
    invalid?: number;
    // Read back by the parent, the runtime the target resolves to
    effectiveRuntime?: string;
    // The weight format the runtime has to accept, when the model is known
    formatId?: string;
    slotPicker?: boolean;
    idPrefix?: string;
    // Fields of the parent's own, placed after the runtime
    children?: Snippet;
  } = $props();

  let showParams = $state(false);

  const slot = $derived(slotId ? live.slots.get(slotId) : undefined);
  const slots = $derived([...live.slots.values()].sort(byName((s) => s.name)));
  const compatible = $derived(cached.runtimes.filter((r) => r.compatible && (!formatId || r.manifest?.formats.includes(formatId))));
  const others = $derived(cached.runtimes.filter((r) => !compatible.includes(r)));
  // The named runtime, else the slot's, else the first that serves the format
  const resolved = $derived(runtimeId || slot?.runtimeId || (formatId ? (compatible[0]?.manifest?.id ?? '') : ''));
  const manifest = $derived(cached.runtimes.find((r) => r.manifest?.id === resolved)?.manifest);
  const inherited = $derived(slot?.params ?? {});
  const set = $derived(Object.keys(values).length);

  $effect(() => {
    effectiveRuntime = resolved;
  });
  // Values already set are worth seeing
  $effect(() => {
    if (set > 0) showParams = true;
  });
</script>

{#if slotPicker && (slots.length || slotId)}
  <Field label="Slot" for="{idPrefix}-slot">
    <Select
      id="{idPrefix}-slot"
      bind:value={slotId}
      items={[{ value: '', label: 'Standalone' }, ...slots.map((s) => ({ value: s.id, label: s.name, detail: slotOccupied(s.id) ? (s.request?.repo ?? 'serving') : 'empty' }))]}
    />
  </Field>
{/if}
<Field label="Runtime" for="{idPrefix}-runtime" error={!compatible.length ? (formatId ? `No compatible runtime serves ${formatId}` : 'No compatible runtime') : undefined}>
  <Select
    id="{idPrefix}-runtime"
    bind:value={runtimeId}
    items={[
      { value: '', label: slot?.runtimeId ? 'Slot default' : 'Auto', detail: slot?.runtimeId || (formatId && compatible[0] ? compatible[0].manifest?.id : 'first compatible') },
      ...compatible.map((rt) => ({ value: rt.manifest?.id ?? '', label: rt.manifest?.name ?? rt.manifest?.id ?? '' })),
      ...others.map((rt) => ({ value: rt.manifest?.id ?? '', label: rt.manifest?.name ?? rt.manifest?.id ?? '', detail: rt.compatible ? 'wrong format' : 'incompatible', disabled: true }))
    ]}
  />
</Field>
{@render children?.()}
<div class="sm:col-span-2">
  <Disclosure label="Parameters" summary={set ? `${set} set` : slot && Object.keys(inherited).length ? 'the slot defaults' : 'the runtime defaults'} bind:open={showParams}>
    <ParamForm params={manifest?.params ?? []} bind:values bind:invalid {inherited} {idPrefix} />
  </Disclosure>
</div>
