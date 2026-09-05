<script lang="ts">
  import { api } from '$lib/api';
  import { live, cached } from '$lib/state.svelte';
  import { createForm } from '$lib/form.svelte';
  import type { Profile } from '$proto/runtime_pb';
  import FormDialog from './ui/FormDialog.svelte';
  import Field from './ui/Field.svelte';
  import CheckCard from './ui/CheckCard.svelte';
  import ParamForm from './ParamForm.svelte';

  let { open = $bindable(false), editing = null, runtimeId = '' }: { open?: boolean; editing?: Profile | null; runtimeId?: string } = $props();

  let rt = $state('');
  let name = $state('');
  let description = $state('');
  let isDefault = $state(false);
  let values = $state<Record<string, string>>({});
  let invalid = $state(0);

  const manifest = $derived(cached.runtimes.find((r) => r.manifest?.id === rt)?.manifest);
  const nameTaken = $derived([...live.profiles.values()].some((p) => p.runtimeId === rt && p.id !== editing?.id && p.name.toLowerCase() === name.trim().toLowerCase()));

  // Params belong to a runtime, so switching runtimes starts them over
  function pick(id: string) {
    rt = id;
    values = {};
  }

  const form = createForm({
    open: () => open,
    close: () => (open = false),
    reset() {
      rt = editing?.runtimeId || runtimeId || cached.runtimes[0]?.manifest?.id || '';
      name = editing?.name ?? '';
      description = editing?.description ?? '';
      isDefault = editing?.default ?? false;
      values = { ...(editing?.params ?? {}) };
    },
    async submit() {
      const profile = { id: editing?.id ?? '', runtimeId: rt, name: name.trim(), description: description.trim(), params: values, default: isDefault };
      if (editing) {
        await api.runtimes.updateProfile({ profile });
        return { title: `Updated ${profile.name}`, detail: 'Applies to the next run that uses it' };
      }
      await api.runtimes.createProfile({ profile });
      return { title: `Added ${profile.name}`, detail: isDefault ? `Every ${rt} run starts from it now` : 'Pick it in the run dialog or pass --profile' };
    },
    failTitle: () => (editing ? 'Update failed' : 'Add failed')
  });
</script>

<FormDialog
  bind:open
  title={editing ? `Edit ${editing.name}` : 'New profile'}
  description={editing ? `${editing.runtimeId} · ${editing.id}` : 'A named set of params for one runtime, kept beside the seeded manifest'}
  size="lg"
  action={editing ? 'Save' : 'Add profile'}
  saving={form.saving}
  disabled={!rt || !name.trim() || nameTaken || invalid > 0}
  onsubmit={form.run}
>
  <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
    <Field label="Runtime" for="prof-runtime" hint={editing ? 'A profile stays with its runtime' : manifest?.description}>
      <select id="prof-runtime" class="input" value={rt} disabled={!!editing} onchange={(e) => pick((e.currentTarget as HTMLSelectElement).value)}>
        {#each cached.runtimes as r (r.manifest?.id)}<option value={r.manifest?.id}>{r.manifest?.name ?? r.manifest?.id}</option>{/each}
      </select>
    </Field>
    <Field label="Name" for="prof-name" hint="Unique within the runtime, usable as --profile on the command line">
      <input id="prof-name" class="input font-mono" bind:value={name} placeholder="long-context" aria-invalid={nameTaken} autocomplete="off" spellcheck="false" />
      {#if nameTaken}<span class="text-xs text-bad">{rt} already has a profile with this name</span>{/if}
    </Field>
    <Field label="Description" for="prof-desc" class="sm:col-span-2">
      <input id="prof-desc" class="input" bind:value={description} placeholder="Optional" />
    </Field>
    <CheckCard bind:checked={isDefault} class="sm:col-span-2" title="Default for {rt || 'the runtime'}" description="Every run, swap, and fit check of the runtime that names no profile starts from these params. One profile per runtime holds this." />
  </div>

  <div class="mt-5 mb-2 text-[10.5px] font-semibold tracking-wider text-fg-faint uppercase">Parameters</div>
  <p class="mb-3 text-xs text-fg-muted">Empty fields inherit the manifest default. Slot defaults and a run's own params apply over the profile.</p>
  <ParamForm params={manifest?.params ?? []} bind:values bind:invalid idPrefix="prof" />
</FormDialog>
