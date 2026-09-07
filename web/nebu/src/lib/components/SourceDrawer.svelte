<script lang="ts">
  import { api } from '$lib/api';
  import { createForm } from '$lib/form.svelte';
  import { humanize } from '$lib/catalog';
  import { confirm } from '$lib/confirm.svelte';
  import { fail, ok } from '$lib/toast.svelte';
  import { SourceKind, type ConfigField, type Provider, type SourceStatus } from '$proto/source_pb';
  import { Trash2 } from '@lucide/svelte';
  import Drawer from './ui/Drawer.svelte';
  import Button from './ui/Button.svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Section from './ui/Section.svelte';
  import ConfigForm from './ConfigForm.svelte';

  // A source added or edited, its settings the form its provider declares
  let { open = $bindable(false), providers = [], editing = null }: { open?: boolean; providers?: Provider[]; editing?: SourceStatus | null } = $props();

  let kindText = $state('');
  let id = $state('');
  let name = $state('');
  let config = $state<Record<string, string>>({});

  const kind = $derived(Number(kindText || SourceKind.UNSPECIFIED) as SourceKind);
  const provider = $derived(providers.find((p) => p.kind === kind));
  // The provider declares the form, an existing source carrying it in its capabilities
  const fields = $derived<ConfigField[]>(editing ? (editing.capabilities?.fields ?? []) : (provider?.fields ?? []));
  const transports = $derived([...new Set(fields.map((f) => f.transport))]);
  const missing = $derived(fields.filter((f) => f.required && !(config[f.name] ?? '').trim() && !f.default).map((f) => f.label || f.name));
  const idOk = $derived(/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(id) && !id.includes('..'));

  // Switching providers starts the settings over, they belong to the provider
  $effect(() => {
    void kindText;
    if (!editing) config = {};
  });

  const acronyms = new Set(['http', 'cli', 'api', 'oci', 'ngc', 'ssh', 'lfs']);
  const transportName = (t: string) => (acronyms.has(t.toLowerCase()) ? t.toUpperCase() : humanize(t));

  const form = createForm({
    open: () => open,
    close: () => (open = false),
    reset() {
      kindText = String(editing?.source?.kind ?? providers[0]?.kind ?? SourceKind.UNSPECIFIED);
      id = editing?.source?.id ?? '';
      name = editing?.source?.name ?? '';
      config = { ...(editing?.source?.config ?? {}) };
    },
    async submit() {
      const settings: Record<string, string> = {};
      for (const [k, v] of Object.entries(config)) if (v.trim()) settings[k] = v.trim();
      const source = { id: id.trim(), kind, name: name.trim(), config: settings };
      if (editing) {
        await api.sources.updateSource({ source });
        return { title: `Updated ${source.name || source.id}` };
      }
      await api.sources.createSource({ source });
      return { title: `Added ${source.name || source.id}` };
    },
    failTitle: () => (editing ? 'Update failed' : 'Add failed')
  });

  const disabled = $derived((!editing && (!idOk || !provider)) || missing.length > 0);

  async function remove() {
    if (!editing?.source) return;
    const label = editing.source.name || editing.source.id;
    const yes = await confirm({ title: `Remove ${label}?`, message: 'Models it pulled stay in the library.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.sources.deleteSource({ id: editing.source.id });
      ok(`Removed ${label}`);
      open = false;
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }
</script>

<Drawer bind:open title={editing ? editing.source?.name || editing.source?.id || 'Source' : 'New source'} subtitle={editing ? `${editing.capabilities?.name ?? ''} · ${editing.source?.id ?? ''}` : 'A place models come from'}>
  <form
    class="flex flex-col gap-6 px-6 py-5"
    onsubmit={(e) => {
      e.preventDefault();
      if (!disabled && !form.saving) form.run();
    }}
  >
    <div class="grid grid-cols-1 gap-x-4 gap-y-5 sm:grid-cols-2">
      {#if !editing}
        <Field label="Provider" for="src-kind" hint={provider?.description || undefined} class="sm:col-span-2">
          {#if providers.length <= 1}
            <div id="src-kind" class="input-static">{provider?.name ?? '–'}</div>
          {:else}
            <Select id="src-kind" bind:value={kindText} items={providers.map((p) => ({ value: String(p.kind), label: p.name }))} />
          {/if}
        </Field>
        <Field label="Id" for="src-id" hint="Fixed once created" error={id && !idOk ? 'Letters, digits, dots, dashes, and underscores' : undefined}>
          <input id="src-id" class="input font-mono" bind:value={id} placeholder="my-source" aria-invalid={!!id && !idOk} autocomplete="off" spellcheck="false" />
        </Field>
      {/if}
      <Field label="Name" for="src-name" class={editing ? 'sm:col-span-2' : ''}>
        <input id="src-name" class="input" bind:value={name} placeholder={provider?.name} autocomplete="off" />
      </Field>
    </div>

    {#each transports as t (t)}
      <Section title={transports.length > 1 ? transportName(t) : 'Settings'}>
        <ConfigForm fields={fields.filter((f) => f.transport === t)} bind:values={config} idPrefix="src-{t}" />
      </Section>
    {/each}
    {#if fields.length === 0 && provider}
      <p class="text-sm text-fg-faint">Nothing to configure</p>
    {/if}
    {#if editing?.error}
      <div class="note note-bad">{editing.error}</div>
    {/if}
    <button type="submit" class="hidden" aria-hidden="true" tabindex="-1"></button>
  </form>

  {#snippet footer()}
    {#if editing && !editing.source?.seeded}
      <Button variant="ghost" size="sm" icon={Trash2} class="text-bad hover:text-bad" onclick={remove}>Remove</Button>
    {:else if editing?.source?.seeded}
      <span class="text-xs text-fg-faint">Seeded by the daemon, editable but not removable</span>
    {/if}
    {#if missing.length}<span class="text-sm text-warn">Needs {missing.join(' and ')}</span>{/if}
    <span class="ml-auto flex gap-2">
      <Button variant="ghost" size="sm" onclick={() => (open = false)}>Cancel</Button>
      <Button variant="primary" size="sm" loading={form.saving} {disabled} onclick={form.run}>{editing ? 'Save' : 'Add'}</Button>
    </span>
  {/snippet}
</Drawer>
