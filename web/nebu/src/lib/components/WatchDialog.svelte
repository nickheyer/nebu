<script lang="ts">
  import { api } from '$lib/api';
  import { live, profilesOf, profileParams } from '$lib/state.svelte';
  import { fail, ok } from '$lib/toast.svelte';
  import type { Source } from '$proto/source_pb';
  import type { RuntimeStatus } from '$proto/runtime_pb';
  import Dialog from './ui/Dialog.svelte';
  import Field from './ui/Field.svelte';
  import Button from './ui/Button.svelte';
  import ParamForm from './ParamForm.svelte';

  let { open = $bindable(false), sourceId = '', repo = '' }: { open?: boolean; sourceId?: string; repo?: string } = $props();

  let sources = $state<Source[]>([]);
  let source = $state('');
  let repoText = $state('');
  let revision = $state('');
  let match = $state('');
  let autoPull = $state(false);
  let slotId = $state('');
  let runtimeId = $state('');
  let profileId = $state('');
  let values = $state<Record<string, string>>({});
  let runtimes = $state<RuntimeStatus[]>([]);
  let saving = $state(false);

  const slot = $derived(slotId ? live.slots.get(slotId) : undefined);
  // The swap runs on the named runtime, else the slot's, so its params take that shape
  const effectiveRuntime = $derived(runtimeId || slot?.runtimeId || '');
  const manifest = $derived(runtimes.find((r) => r.manifest?.id === effectiveRuntime)?.manifest);
  const profiles = $derived(profilesOf(effectiveRuntime));
  const defaultProfile = $derived(profiles.find((p) => p.default));
  const inherited = $derived({ ...profileParams(effectiveRuntime, profileId), ...(slot?.params ?? {}) });

  $effect(() => {
    void effectiveRuntime;
    profileId = '';
  });

  $effect(() => {
    if (!open) return;
    source = sourceId;
    repoText = repo;
    revision = match = slotId = runtimeId = profileId = '';
    values = {};
    autoPull = false;
    api.runtimes.listRuntimes({}).then((r) => (runtimes = r.runtimes)).catch(() => (runtimes = []));
    api.sources
      .listSources({})
      .then((r) => {
        sources = r.sources.flatMap((s) => (s.source ? [s.source] : []));
        if (!source && sources.length) source = sources[0].id;
      })
      .catch(() => (sources = []));
  });

  async function submit() {
    saving = true;
    try {
      await api.monitor.addWatch({ sourceId: source, repo: repoText.trim(), revision, groupMatch: match, autoPull: autoPull || !!slotId, slotId, runtimeId, params: values, profileId });
      ok(`Watching ${repoText.trim()}`, 'The first check ran just now');
      open = false;
    } catch (err) {
      fail(err, 'Watch refused');
    } finally {
      saving = false;
    }
  }
</script>

<Dialog bind:open title="Watch a repository" description="Checks on an interval and records a finding for a new commit or a weight group that appears or vanishes">
  <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
    <Field label="Source" for="w-source">
      <select id="w-source" class="input" bind:value={source}>
        {#each sources as s (s.id)}<option value={s.id}>{s.name || s.id}</option>{/each}
      </select>
    </Field>
    <Field label="Repository" for="w-repo">
      <input id="w-repo" class="input font-mono" bind:value={repoText} placeholder="org/name" />
    </Field>
    <Field label="Revision" for="w-rev" hint="Branch or tag, source default when empty">
      <input id="w-rev" class="input font-mono" bind:value={revision} placeholder="main" />
    </Field>
    <Field label="Group match" for="w-match" hint="Regex over weight group names">
      <input id="w-match" class="input font-mono" bind:value={match} placeholder="Q4_K_M|Q5" />
    </Field>

    <label class="flex cursor-pointer items-start gap-3 rounded-lg border border-line bg-sunken px-3 py-2.5 sm:col-span-2">
      <input type="checkbox" class="mt-0.5 accent-accent" bind:checked={autoPull} />
      <span class="text-sm">
        <span class="font-medium text-fg">Pull automatically</span>
        <span class="block text-xs leading-5 text-fg-muted">Matching groups are pulled when they appear or change. Picking a slot below turns this on.</span>
      </span>
    </label>

    <Field label="Swap into slot" for="w-slot" hint="The slot moves to the freshest pull once it lands">
      <select id="w-slot" class="input" bind:value={slotId}>
        <option value="">No swap</option>
        {#each [...live.slots.values()] as s (s.id)}<option value={s.id}>{s.name}</option>{/each}
      </select>
    </Field>
    <Field label="Runtime for the swap" for="w-runtime">
      <select id="w-runtime" class="input" bind:value={runtimeId} disabled={!slotId}>
        <option value="">{slot?.runtimeId ? `Slot default · ${slot.runtimeId}` : 'First compatible runtime'}</option>
        {#each runtimes as rt (rt.manifest?.id)}
          <option value={rt.manifest?.id}>{rt.manifest?.name ?? rt.manifest?.id}{rt.compatible ? '' : ' · incompatible'}</option>
        {/each}
      </select>
    </Field>
    <Field label="Profile for the swap" for="w-profile" class="sm:col-span-2" hint={slotId && effectiveRuntime && !profiles.length ? `No profiles for ${effectiveRuntime} yet` : ''}>
      <select id="w-profile" class="input" bind:value={profileId} disabled={!slotId || !profiles.length}>
        <option value="">{defaultProfile ? `Runtime default · ${defaultProfile.name}` : 'Manifest defaults'}</option>
        {#each profiles as p (p.id)}<option value={p.id}>{p.name}{p.description ? ` · ${p.description}` : ''}</option>{/each}
      </select>
    </Field>
    {#if slotId}
      <div class="sm:col-span-2">
        <div class="mb-2 flex items-baseline gap-2">
          <span class="text-xs font-medium text-fg-muted">Parameters for the swap</span>
          <span class="text-[11.5px] text-fg-faint">Empty fields inherit the profile, then the slot</span>
        </div>
        <ParamForm params={manifest?.params ?? []} bind:values {inherited} idPrefix="w" />
      </div>
    {/if}
  </div>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button variant="primary" loading={saving} onclick={submit} disabled={!repoText.trim() || !source}>Watch</Button>
  {/snippet}
</Dialog>
