<script lang="ts">
  import { api } from '$lib/api';
  import { live, profilesOf, profileParams } from '$lib/state.svelte';
  import { fail, ok } from '$lib/toast.svelte';
  import { groupByProvider, type ProviderGroup } from '$lib/catalog';
  import { SourceKind, type SourceStatus } from '$proto/source_pb';
  import type { RuntimeStatus } from '$proto/runtime_pb';
  import Dialog from './ui/Dialog.svelte';
  import Field from './ui/Field.svelte';
  import Button from './ui/Button.svelte';
  import ParamForm from './ParamForm.svelte';

  let { open = $bindable(false), query = '' }: { open?: boolean; query?: string } = $props();

  let statuses = $state<SourceStatus[]>([]);
  let runtimes = $state<RuntimeStatus[]>([]);
  let text = $state('');
  // Where to look: everything, one provider as kind:N, or one source as source:ID
  let where = $state('');
  let match = $state('');
  let formatId = $state('');
  let autoPull = $state(false);
  let slotId = $state('');
  let runtimeId = $state('');
  let profileId = $state('');
  let values = $state<Record<string, string>>({});
  let invalid = $state(0);
  let saving = $state(false);

  const groups = $derived<ProviderGroup[]>(groupByProvider(statuses));
  const slot = $derived(slotId ? live.slots.get(slotId) : undefined);
  // The swap runs on the named runtime, else the slot's, else the one a picked profile belongs to
  const pickedRuntime = $derived(runtimeId || slot?.runtimeId || '');
  const profileRuntime = $derived(live.profiles.get(profileId)?.runtimeId ?? '');
  const effectiveRuntime = $derived(pickedRuntime || profileRuntime);
  const manifest = $derived(runtimes.find((r) => r.manifest?.id === effectiveRuntime)?.manifest);
  const profiles = $derived(profilesOf(pickedRuntime));
  const defaultProfile = $derived(pickedRuntime ? profiles.find((p) => p.default) : undefined);
  const inherited = $derived({ ...profileParams(effectiveRuntime, profileId), ...(slot?.params ?? {}) });

  // A profile belongs to one runtime, so it drops when the runtime moves away from it
  $effect(() => {
    if (profileId && (!live.profiles.has(profileId) || profileRuntime !== effectiveRuntime)) profileId = '';
  });

  $effect(() => {
    if (!open) return;
    text = query;
    where = match = formatId = slotId = runtimeId = profileId = '';
    values = {};
    autoPull = false;
    api.sources.listSources({}).then((r) => (statuses = r.sources)).catch(() => (statuses = []));
    api.runtimes.listRuntimes({}).then((r) => (runtimes = r.runtimes)).catch(() => (runtimes = []));
  });

  async function submit() {
    saving = true;
    const [scope, id] = where.split(':');
    try {
      await api.monitor.addWant({
        query: text.trim(),
        kind: scope === 'kind' ? (Number(id) as SourceKind) : SourceKind.UNSPECIFIED,
        sourceId: scope === 'source' ? id : '',
        groupMatch: match,
        formatId,
        autoPull: autoPull || !!slotId,
        slotId,
        runtimeId,
        params: values,
        profileId
      });
      ok(`Wanting ${text.trim()}`, 'The first look is running now, then every monitor interval');
      open = false;
    } catch (err) {
      fail(err, 'Want refused');
    } finally {
      saving = false;
    }
  }
</script>

<Dialog bind:open title="Want a model" description="A standing search across your sources, satisfied the moment a repository with a matching weight group turns up" size="lg">
  <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
    <Field label="Search for" for="want-query" hint="Words the catalog search takes, the first hits are checked each time" class="sm:col-span-2">
      <input id="want-query" class="input" bind:value={text} placeholder="model name, family, or format" autocomplete="off" spellcheck="false" />
    </Field>
    <Field label="Where" for="want-where">
      <select id="want-where" class="input" bind:value={where}>
        <option value="">Every source</option>
        {#each groups as g (g.kind)}
          <option value="kind:{g.kind}">{g.name}{g.sources.length > 1 ? ` · all ${g.sources.length}` : ''}</option>
          {#if g.sources.length > 1}
            {#each g.sources as s (s.source?.id)}<option value="source:{s.source?.id}">&nbsp;&nbsp;{s.source?.name || s.source?.id}</option>{/each}
          {/if}
        {/each}
      </select>
    </Field>
    <Field label="Format" for="want-format" hint="Only weight groups of this format satisfy it">
      <select id="want-format" class="input" bind:value={formatId}>
        <option value="">Any format</option>
        {#each [...live.formats.values()] as f (f.id)}<option value={f.id}>{f.description || f.id}</option>{/each}
      </select>
    </Field>
    <Field label="Group match" for="want-match" hint="Regex over weight group names, any when empty" class="sm:col-span-2">
      <input id="want-match" class="input font-mono" bind:value={match} placeholder="regex" autocomplete="off" spellcheck="false" />
    </Field>

    <label class="flex cursor-pointer items-start gap-3 rounded-lg border border-line bg-sunken px-3 py-2.5 sm:col-span-2">
      <input type="checkbox" class="mt-0.5 accent-accent" bind:checked={autoPull} />
      <span class="text-sm">
        <span class="font-medium text-fg">Pull when found</span>
        <span class="block text-xs leading-5 text-fg-muted">The matching group is pulled into the store as soon as it turns up. Picking a slot below turns this on.</span>
      </span>
    </label>

    <Field label="Swap into slot" for="want-slot" hint="The slot moves to the pull once it lands">
      <select id="want-slot" class="input" bind:value={slotId}>
        <option value="">No swap</option>
        {#each [...live.slots.values()] as s (s.id)}<option value={s.id}>{s.name}</option>{/each}
      </select>
    </Field>
    <Field label="Runtime for the swap" for="want-runtime">
      <select id="want-runtime" class="input" bind:value={runtimeId} disabled={!slotId}>
        <option value="">{slot?.runtimeId ? `Slot default · ${slot.runtimeId}` : 'First compatible runtime'}</option>
        {#each runtimes as rt (rt.manifest?.id)}
          <option value={rt.manifest?.id}>{rt.manifest?.name ?? rt.manifest?.id}{rt.compatible ? '' : ' · incompatible'}</option>
        {/each}
      </select>
    </Field>
    <Field label="Profile for the swap" for="want-profile" class="sm:col-span-2" hint={slotId && !profiles.length ? `No profiles for ${pickedRuntime || 'any runtime'} yet` : slotId && !pickedRuntime ? 'A profile picks its runtime when the slot names none' : ''}>
      <select id="want-profile" class="input" bind:value={profileId} disabled={!slotId || !profiles.length}>
        <option value="">{defaultProfile ? `Runtime default · ${defaultProfile.name}` : 'Manifest defaults'}</option>
        {#each profiles as p (p.id)}<option value={p.id}>{pickedRuntime ? '' : `${p.runtimeId} · `}{p.name}{p.description ? ` · ${p.description}` : ''}</option>{/each}
      </select>
    </Field>
    {#if slotId}
      <div class="sm:col-span-2">
        <div class="mb-2 flex items-baseline gap-2">
          <span class="text-xs font-medium text-fg-muted">Parameters for the swap</span>
          <span class="text-[11.5px] text-fg-faint">Empty fields inherit the profile, then the slot</span>
        </div>
        <ParamForm params={manifest?.params ?? []} bind:values bind:invalid {inherited} idPrefix="want" />
      </div>
    {/if}
  </div>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button variant="primary" loading={saving} onclick={submit} disabled={!text.trim() || invalid > 0}>Want it</Button>
  {/snippet}
</Dialog>
