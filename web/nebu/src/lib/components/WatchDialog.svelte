<script lang="ts">
  import { api } from '$lib/api';
  import { live, profilesOf, profileParams } from '$lib/state.svelte';
  import { fail, ok } from '$lib/toast.svelte';
  import type { SourceStatus } from '$proto/source_pb';
  import type { RuntimeStatus } from '$proto/runtime_pb';
  import Dialog from './ui/Dialog.svelte';
  import Field from './ui/Field.svelte';
  import Button from './ui/Button.svelte';
  import ParamForm from './ParamForm.svelte';

  let { open = $bindable(false), sourceId = '', repo = '' }: { open?: boolean; sourceId?: string; repo?: string } = $props();

  let sources = $state<SourceStatus[]>([]);
  let source = $state('');
  let repoText = $state('');
  let revision = $state('');
  let match = $state('');
  let autoPull = $state(false);
  let slotId = $state('');
  let runtimeId = $state('');
  let profileId = $state('');
  let values = $state<Record<string, string>>({});
  let invalid = $state(0);
  let runtimes = $state<RuntimeStatus[]>([]);
  let saving = $state(false);

  // The repository form the chosen source takes, as its provider states it
  const repoExample = $derived(sources.find((s) => s.source?.id === source)?.capabilities?.repoExample || 'owner/name');
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
    source = sourceId;
    repoText = repo;
    revision = match = slotId = runtimeId = profileId = '';
    values = {};
    autoPull = false;
    api.runtimes.listRuntimes({}).then((r) => (runtimes = r.runtimes)).catch(() => (runtimes = []));
    api.sources
      .listSources({})
      .then((r) => {
        sources = r.sources.filter((s) => s.source);
        if (!source && sources.length) source = sources[0].source?.id ?? '';
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
        {#each sources as s (s.source?.id)}<option value={s.source?.id}>{s.source?.name || s.source?.id}</option>{/each}
      </select>
    </Field>
    <Field label="Repository" for="w-repo">
      <input id="w-repo" class="input font-mono" bind:value={repoText} placeholder={repoExample} />
    </Field>
    <Field label="Revision" for="w-rev" hint="Branch or tag, source default when empty">
      <input id="w-rev" class="input font-mono" bind:value={revision} placeholder="main" />
    </Field>
    <Field label="Group match" for="w-match" hint="Regex over weight group names">
      <input id="w-match" class="input font-mono" bind:value={match} placeholder="regex" />
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
    <Field label="Profile for the swap" for="w-profile" class="sm:col-span-2" hint={slotId && !profiles.length ? `No profiles for ${pickedRuntime || 'any runtime'} yet` : slotId && !pickedRuntime ? 'A profile picks its runtime when the slot names none' : ''}>
      <select id="w-profile" class="input" bind:value={profileId} disabled={!slotId || !profiles.length}>
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
        <ParamForm params={manifest?.params ?? []} bind:values bind:invalid {inherited} idPrefix="w" />
      </div>
    {/if}
  </div>

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button variant="primary" loading={saving} onclick={submit} disabled={!repoText.trim() || !source || invalid > 0}>Watch</Button>
  {/snippet}
</Dialog>
