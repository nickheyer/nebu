<script lang="ts">
  import { api } from '$lib/api';
  import { live, cached } from '$lib/state.svelte';
  import { createForm } from '$lib/form.svelte';
  import type { Profile } from '$proto/runtime_pb';
  import FormDialog from './ui/FormDialog.svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Checkbox from './ui/Checkbox.svelte';
  import Section from './ui/Section.svelte';
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
  $effect(() => {
    void rt;
    if (!editing) values = {};
  });

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
        return { title: `Updated ${profile.name}` };
      }
      await api.runtimes.createProfile({ profile });
      return { title: `Added ${profile.name}` };
    },
    failTitle: () => (editing ? 'Update failed' : 'Add failed')
  });
</script>

<FormDialog
  bind:open
  title={editing ? `Edit ${editing.name}` : 'New profile'}
  subtitle={editing ? editing.runtimeId : undefined}
  size="lg"
  action={editing ? 'Save' : 'Add'}
  saving={form.saving}
  disabled={!rt || !name.trim() || nameTaken || invalid > 0}
  onsubmit={form.run}
>
  <div class="flex flex-col gap-6">
    <div class="grid grid-cols-1 gap-x-4 gap-y-5 sm:grid-cols-2">
      <Field label="Runtime" for="prof-runtime">
        {#if editing || cached.runtimes.length <= 1}
          <div id="prof-runtime" class="input-static">{cached.runtimes.find((r) => r.manifest?.id === rt)?.manifest?.name ?? rt ?? '–'}</div>
        {:else}
          <Select id="prof-runtime" bind:value={rt} items={cached.runtimes.map((r) => ({ value: r.manifest?.id ?? '', label: r.manifest?.name ?? r.manifest?.id ?? '' }))} />
        {/if}
      </Field>
      <Field label="Name" for="prof-name" error={nameTaken ? 'That name is taken' : undefined}>
        <input id="prof-name" class="input font-mono" bind:value={name} placeholder="long-context" aria-invalid={nameTaken} autocomplete="off" spellcheck="false" />
      </Field>
      <Field label="Description" for="prof-desc" class="sm:col-span-2">
        <input id="prof-desc" class="input" bind:value={description} placeholder="Optional" />
      </Field>
      <Checkbox bind:checked={isDefault} class="sm:col-span-2" label="Default for {rt || 'the runtime'}" info="Applies to every run that names no profile" />
    </div>
    <Section title="Parameters" info="Empty fields keep the runtime default">
      <ParamForm params={manifest?.params ?? []} bind:values bind:invalid idPrefix="prof" />
    </Section>
  </div>
</FormDialog>
