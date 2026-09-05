<script lang="ts">
  import type { Snippet } from 'svelte';
  import { live, cached, profilesOf, profileParams } from '$lib/state.svelte';
  import { slotOccupied } from '$lib/launch';
  import { byName } from '$lib/format';
  import Field from './ui/Field.svelte';
  import ParamForm from './ParamForm.svelte';

  let {
    slotId = $bindable(''),
    runtimeId = $bindable(''),
    profileId = $bindable(''),
    values = $bindable({}),
    invalid = $bindable(0),
    effectiveRuntime = $bindable(''),
    formatId = '',
    optional = false,
    idPrefix = 'swap',
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
    idPrefix?: string;
    // Fields of the parent's own, placed after the runtime
    children?: Snippet;
  } = $props();

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

  $effect(() => {
    effectiveRuntime = resolved;
  });
  // A profile belongs to one runtime, so it drops when the runtime changes
  $effect(() => {
    if (profileId && (!live.profiles.has(profileId) || profileRuntime !== resolved)) profileId = '';
  });
</script>

<Field label={optional ? 'Swap into slot' : 'Slot'} for="{idPrefix}-slot" hint={optional ? 'The slot moves to the pull once it lands' : slotOccupied(slotId) ? 'Occupied, so the run becomes a swap' : 'A slot pins devices, budget, and public name'}>
  <select id="{idPrefix}-slot" class="input" bind:value={slotId}>
    <option value="">{optional ? 'No swap' : 'No slot, run on the whole host'}</option>
    {#each slots as s (s.id)}
      <option value={s.id}>{s.name}{slotOccupied(s.id) ? ` · serving ${s.request?.repo ?? ''}` : ' · empty'}</option>
    {/each}
  </select>
</Field>
<Field label={optional ? 'Runtime for the swap' : 'Runtime'} for="{idPrefix}-runtime" hint={off || compatible.length ? '' : formatId ? 'No compatible runtime accepts this format on this host' : 'No runtime is compatible with this host'}>
  <select id="{idPrefix}-runtime" class="input" bind:value={runtimeId} disabled={off}>
    <option value="">{slot?.runtimeId ? `Slot default · ${slot.runtimeId}` : formatId && compatible[0] ? `Auto · ${compatible[0].manifest?.id}` : 'First compatible runtime'}</option>
    {#each compatible as rt (rt.manifest?.id)}
      <option value={rt.manifest?.id}>{rt.manifest?.name ?? rt.manifest?.id}</option>
    {/each}
    {#each others as rt (rt.manifest?.id)}
      <option value={rt.manifest?.id} disabled>{rt.manifest?.name ?? rt.manifest?.id} · {rt.compatible ? 'format not accepted' : 'incompatible host'}</option>
    {/each}
  </select>
</Field>
{@render children?.()}
<Field label={optional ? 'Profile for the swap' : 'Profile'} for="{idPrefix}-profile" class="sm:col-span-2" hint={off ? '' : !profiles.length ? `No profiles for ${pickedRuntime || 'any runtime'} yet, add one on the runtimes page` : pickedRuntime ? 'Named params of the runtime the run starts from' : 'A profile picks its runtime when nothing else names one'}>
  <select id="{idPrefix}-profile" class="input" bind:value={profileId} disabled={off || !profiles.length}>
    <option value="">{defaultProfile ? `Runtime default · ${defaultProfile.name}` : 'Manifest defaults'}</option>
    {#each profiles as p (p.id)}<option value={p.id}>{pickedRuntime ? '' : `${p.runtimeId} · `}{p.name}{p.description ? ` · ${p.description}` : ''}</option>{/each}
  </select>
</Field>
{#if !off}
  <div class="sm:col-span-2">
    <div class="mb-2 flex items-baseline gap-2">
      <span class="text-xs font-medium text-fg-muted">{optional ? 'Parameters for the swap' : 'Parameters'}</span>
      <span class="text-[11.5px] text-fg-faint">Empty fields inherit the profile{slot ? ', then the slot' : ''}. Auto values are solved by the planner</span>
    </div>
    <ParamForm params={manifest?.params ?? []} bind:values bind:invalid {inherited} {idPrefix} />
  </div>
{/if}
