<script lang="ts">
  import type { Snippet } from 'svelte';
  import { live, cached, profilesOf, profileParams } from '$lib/state.svelte';
  import { slotOccupied } from '$lib/launch';
  import { byName } from '$lib/format';
  import { ChevronDown } from '@lucide/svelte';
  import Field from './ui/Field.svelte';
  import ParamForm from './ParamForm.svelte';

  // Where and how a model runs: the slot, the runtime it resolves to, a profile, and the parameters over both.
  // The slot is chosen here as a select unless the parent draws its own picker and binds slotId
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
  <Field label={optional ? 'Swap into slot' : 'Slot'} for="{idPrefix}-slot">
    <select id="{idPrefix}-slot" class="input" bind:value={slotId}>
      <option value="">{optional ? 'No swap, only pull' : 'None, run standalone'}</option>
      {#each slots as s (s.id)}
        <option value={s.id}>{s.name}{slotOccupied(s.id) ? ` · serving ${s.request?.repo ?? ''}` : ' · empty'}</option>
      {/each}
    </select>
  </Field>
{/if}
<Field label="Runtime" for="{idPrefix}-runtime" error={!off && !compatible.length ? (formatId ? `No compatible runtime serves ${formatId}` : 'No compatible runtime') : undefined}>
  <select id="{idPrefix}-runtime" class="input" bind:value={runtimeId} disabled={off}>
    <option value="">{slot?.runtimeId ? `Slot default · ${slot.runtimeId}` : formatId && compatible[0] ? `Auto · ${compatible[0].manifest?.id}` : 'First compatible'}</option>
    {#each compatible as rt (rt.manifest?.id)}
      <option value={rt.manifest?.id}>{rt.manifest?.name ?? rt.manifest?.id}</option>
    {/each}
    {#each others as rt (rt.manifest?.id)}
      <option value={rt.manifest?.id} disabled>{rt.manifest?.name ?? rt.manifest?.id} · {rt.compatible ? 'wrong format' : 'incompatible'}</option>
    {/each}
  </select>
</Field>
{@render children?.()}
<Field label="Profile" for="{idPrefix}-profile" info="A named set of parameters for the runtime">
  <select id="{idPrefix}-profile" class="input" bind:value={profileId} disabled={off || !profiles.length}>
    <option value="">{defaultProfile ? `Default · ${defaultProfile.name}` : profiles.length ? 'Runtime defaults' : 'None'}</option>
    {#each profiles as p (p.id)}<option value={p.id}>{pickedRuntime ? '' : `${p.runtimeId} · `}{p.name}{p.description ? ` · ${p.description}` : ''}</option>{/each}
  </select>
</Field>
{#if !off}
  <div class="sm:col-span-2">
    <button type="button" class="flex w-full items-center gap-2 rounded-lg border border-line bg-bg/40 px-3 py-2 text-left text-sm text-fg hover:border-line-strong" aria-expanded={showParams} onclick={() => (showParams = !showParams)}>
      <ChevronDown size={15} class="text-fg-faint transition-transform {showParams ? 'rotate-180' : ''}" />
      <span class="font-medium">Parameters</span>
      <span class="text-fg-faint">{set ? `${set} set` : 'inheriting the profile and slot'}</span>
    </button>
    {#if showParams}
      <div class="mt-3">
        <ParamForm params={manifest?.params ?? []} bind:values bind:invalid {inherited} {idPrefix} />
      </div>
    {/if}
  </div>
{/if}
