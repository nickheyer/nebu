<script lang="ts">
  import type { Snippet } from 'svelte';
  import { live, cached, profilesOf, profileParams } from '$lib/state.svelte';
  import { slotOccupied } from '$lib/launch';
  import { byName } from '$lib/format';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Disclosure from './ui/Disclosure.svelte';
  import ParamForm from './ParamForm.svelte';

  // Where and how a model runs: the slot, the runtime it resolves to, a profile, and the parameters over both
  let {
    slotId = $bindable(''),
    runtimeId = $bindable(''),
    profileId = $bindable(''),
    values = $bindable({}),
    invalid = $bindable(0),
    effectiveRuntime = $bindable(''),
    formatId = '',
    optional = false,
    slotPicker = true,
    idPrefix = 'run',
    children
  }: {
    slotId?: string;
    runtimeId?: string;
    profileId?: string;
    values?: Record<string, string>;
    invalid?: number;
    // Read back by the parent, the runtime the target resolves to
    effectiveRuntime?: string;
    // The weight format the runtime has to accept, when the model is known
    formatId?: string;
    // A watch or want only swaps once a slot is picked
    optional?: boolean;
    slotPicker?: boolean;
    idPrefix?: string;
    // Fields of the parent's own, placed after the runtime
    children?: Snippet;
  } = $props();

  let showParams = $state(false);

  const slot = $derived(slotId ? live.slots.get(slotId) : undefined);
  const off = $derived(optional && !slotId);
  const slots = $derived([...live.slots.values()].sort(byName((s) => s.name)));
  const compatible = $derived(cached.runtimes.filter((r) => r.compatible && (!formatId || r.manifest?.formats.includes(formatId))));
  const others = $derived(cached.runtimes.filter((r) => !compatible.includes(r)));
  // The named runtime, else the slot's, else the first that serves a known format
  const pickedRuntime = $derived(runtimeId || slot?.runtimeId || (formatId ? (compatible[0]?.manifest?.id ?? '') : ''));
  const profileRuntime = $derived(live.profiles.get(profileId)?.runtimeId ?? '');
  // A picked profile names the runtime when nothing else does
  const resolved = $derived(pickedRuntime || profileRuntime);
  const manifest = $derived(cached.runtimes.find((r) => r.manifest?.id === resolved)?.manifest);
  const profiles = $derived(profilesOf(pickedRuntime));
  const defaultProfile = $derived(pickedRuntime ? profiles.find((p) => p.default) : undefined);
  const inherited = $derived({ ...profileParams(resolved, profileId), ...(slot?.params ?? {}) });
  const set = $derived(Object.keys(values).length);

  $effect(() => {
    effectiveRuntime = resolved;
  });
  // A profile belongs to one runtime, so it drops when the runtime changes
  $effect(() => {
    if (profileId && (!live.profiles.has(profileId) || profileRuntime !== resolved)) profileId = '';
  });
  // Values already set are worth seeing
  $effect(() => {
    if (set > 0) showParams = true;
  });
</script>

{#if slotPicker && (slots.length || slotId)}
  <Field label={optional ? 'Swap into' : 'Slot'} for="{idPrefix}-slot">
    <Select
      id="{idPrefix}-slot"
      bind:value={slotId}
      items={[{ value: '', label: optional ? 'No swap' : 'Standalone' }, ...slots.map((s) => ({ value: s.id, label: s.name, detail: slotOccupied(s.id) ? (s.request?.repo ?? 'serving') : 'empty' }))]}
    />
  </Field>
{/if}
<Field label="Runtime" for="{idPrefix}-runtime" error={!off && !compatible.length ? (formatId ? `No compatible runtime serves ${formatId}` : 'No compatible runtime') : undefined}>
  <Select
    id="{idPrefix}-runtime"
    bind:value={runtimeId}
    disabled={off}
    items={[
      { value: '', label: slot?.runtimeId ? 'Slot default' : 'Auto', detail: slot?.runtimeId || (formatId && compatible[0] ? compatible[0].manifest?.id : 'first compatible') },
      ...compatible.map((rt) => ({ value: rt.manifest?.id ?? '', label: rt.manifest?.name ?? rt.manifest?.id ?? '' })),
      ...others.map((rt) => ({ value: rt.manifest?.id ?? '', label: rt.manifest?.name ?? rt.manifest?.id ?? '', detail: rt.compatible ? 'wrong format' : 'incompatible', disabled: true }))
    ]}
  />
</Field>
{@render children?.()}
<Field label="Profile" for="{idPrefix}-profile" info="A named set of parameters for the runtime">
  <Select
    id="{idPrefix}-profile"
    bind:value={profileId}
    disabled={off || !profiles.length}
    items={[
      { value: '', label: defaultProfile ? 'Default' : profiles.length ? 'Runtime defaults' : 'None', detail: defaultProfile?.name },
      ...profiles.map((p) => ({ value: p.id, label: p.name, detail: [pickedRuntime ? '' : p.runtimeId, p.description].filter(Boolean).join(' · ') || undefined }))
    ]}
  />
</Field>
{#if !off}
  <div class="sm:col-span-2">
    <Disclosure label="Parameters" summary={set ? `${set} set` : 'inherited'} bind:open={showParams}>
      <ParamForm params={manifest?.params ?? []} bind:values bind:invalid {inherited} {idPrefix} />
    </Disclosure>
  </div>
{/if}
