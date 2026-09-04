<script lang="ts">
  import { api } from '$lib/api';
  import { live } from '$lib/state.svelte';
  import { fail, ok } from '$lib/toast.svelte';
  import type { Profile, RuntimeStatus } from '$proto/runtime_pb';
  import Dialog from './ui/Dialog.svelte';
  import Field from './ui/Field.svelte';
  import Button from './ui/Button.svelte';
  import ParamForm from './ParamForm.svelte';

  let {
    open = $bindable(false),
    runtimes = [],
    editing = null,
    runtimeId = ''
  }: { open?: boolean; runtimes?: RuntimeStatus[]; editing?: Profile | null; runtimeId?: string } = $props();

  let rt = $state('');
  let name = $state('');
  let description = $state('');
  let isDefault = $state(false);
  let values = $state<Record<string, string>>({});
  let invalid = $state(0);
  let saving = $state(false);

  const manifest = $derived(runtimes.find((r) => r.manifest?.id === rt)?.manifest);
  const nameTaken = $derived([...live.profiles.values()].some((p) => p.runtimeId === rt && p.id !== editing?.id && p.name.toLowerCase() === name.trim().toLowerCase()));

  $effect(() => {
    if (!open) return;
    rt = editing?.runtimeId || runtimeId || runtimes[0]?.manifest?.id || '';
    name = editing?.name ?? '';
    description = editing?.description ?? '';
    isDefault = editing?.default ?? false;
    values = { ...(editing?.params ?? {}) };
  });

  // Params belong to a runtime, so switching runtimes starts them over
  function pick(id: string) {
    rt = id;
    values = {};
  }

  async function submit() {
    saving = true;
    const profile = { id: editing?.id ?? '', runtimeId: rt, name: name.trim(), description: description.trim(), params: values, default: isDefault };
    try {
      if (editing) {
        await api.runtimes.updateProfile({ profile });
        ok(`Updated ${profile.name}`, 'Applies to the next run that uses it');
      } else {
        await api.runtimes.createProfile({ profile });
        ok(`Added ${profile.name}`, isDefault ? `Every ${rt} run starts from it now` : `Pick it in the run dialog or pass --profile`);
      }
      open = false;
    } catch (err) {
      fail(err, editing ? 'Update failed' : 'Add failed');
    } finally {
      saving = false;
    }
  }
</script>

<Dialog bind:open title={editing ? `Edit ${editing.name}` : 'New profile'} description={editing ? `${editing.runtimeId} · ${editing.id}` : 'A named set of params for one runtime, kept beside the seeded manifest'} size="lg">
  <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
    <Field label="Runtime" for="prof-runtime" hint={editing ? 'A profile stays with its runtime' : manifest?.description}>
      <select id="prof-runtime" class="input" value={rt} disabled={!!editing} onchange={(e) => pick((e.currentTarget as HTMLSelectElement).value)}>
        {#each runtimes as r (r.manifest?.id)}<option value={r.manifest?.id}>{r.manifest?.name ?? r.manifest?.id}</option>{/each}
      </select>
    </Field>
    <Field label="Name" for="prof-name" hint="Unique within the runtime, usable as --profile on the command line">
      <input id="prof-name" class="input font-mono" bind:value={name} placeholder="long-context" aria-invalid={nameTaken} autocomplete="off" spellcheck="false" />
      {#if nameTaken}<span class="text-xs text-bad">{rt} already has a profile with this name</span>{/if}
    </Field>
    <Field label="Description" for="prof-desc" class="sm:col-span-2">
      <input id="prof-desc" class="input" bind:value={description} placeholder="Optional" />
    </Field>
    <label class="flex cursor-pointer items-start gap-3 rounded-lg border border-line bg-sunken px-3 py-2.5 sm:col-span-2">
      <input type="checkbox" class="mt-0.5 accent-accent" bind:checked={isDefault} />
      <span class="text-sm">
        <span class="font-medium text-fg">Default for {rt || 'the runtime'}</span>
        <span class="block text-xs leading-5 text-fg-muted">Every run, swap, and fit check of the runtime that names no profile starts from these params. One profile per runtime holds this.</span>
      </span>
    </label>
  </div>

  <div class="mt-5 mb-2 text-[10.5px] font-semibold tracking-wider text-fg-faint uppercase">Parameters</div>
  <p class="mb-3 text-xs text-fg-muted">Empty fields inherit the manifest default. Slot defaults and a run's own params apply over the profile.</p>
  <ParamForm params={manifest?.params ?? []} bind:values bind:invalid idPrefix="prof" />

  {#snippet footer()}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button variant="primary" loading={saving} onclick={submit} disabled={!rt || !name.trim() || nameTaken || invalid > 0}>{editing ? 'Save' : 'Add profile'}</Button>
  {/snippet}
</Dialog>
