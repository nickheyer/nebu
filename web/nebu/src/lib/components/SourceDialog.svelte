<script lang="ts">
  import { api } from '$lib/api';
  import { createForm } from '$lib/form.svelte';
  import { humanize } from '$lib/catalog';
  import { confirm } from '$lib/confirm.svelte';
  import { fail, ok } from '$lib/toast.svelte';
  import { SourceKind, type ConfigField, type Provider, type SourceStatus } from '$proto/source_pb';
  import { Trash2 } from '@lucide/svelte';
  import Dialog from './ui/Dialog.svelte';
  import Button from './ui/Button.svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import ConfigForm from './ConfigForm.svelte';
  import TextInput from './ui/TextInput.svelte';

  let { open = $bindable(false), providers = [], editing = null }: { open?: boolean; providers?: Provider[]; editing?: SourceStatus | null } = $props();

  let kindText = $state('');
  let id = $state('');
  let name = $state('');
  let config = $state<Record<string, string>>({});

  const kind = $derived(Number(kindText || SourceKind.UNSPECIFIED) as SourceKind);
  const provider = $derived(providers.find((p) => p.kind === kind));
  const fields = $derived<ConfigField[]>(editing ? (editing.capabilities?.fields ?? []) : (provider?.fields ?? []));
  const transports = $derived([...new Set(fields.map((f) => f.transport))]);
  const missing = $derived(fields.filter((f) => f.required && !(config[f.name] ?? '').trim() && !f.default).map((f) => f.label || f.name));
  const idOk = $derived(/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(id) && !id.includes('..'));
  const idHint = $derived(
    (provider?.name ?? '')
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '')
  );

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
        return { title: `Saved ${source.name || source.id}` };
      }
      await api.sources.createSource({ source });
      return { title: `Added ${source.name || source.id}` };
    },
    failTitle: () => (editing ? 'Save failed' : 'Add failed')
  });

  const disabled = $derived((!editing && (!idOk || !provider)) || missing.length > 0);

  async function remove() {
    if (!editing?.source) return;
    const label = editing.source.name || editing.source.id;
    const yes = await confirm({ title: `Remove ${label}?`, message: 'Models pulled from it stay in the library.', action: 'Remove', tone: 'bad' });
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

<Dialog bind:open size="lg" title={editing ? editing.source?.name || editing.source?.id || 'Source' : 'Add source'} description={editing ? `${editing.capabilities?.name ?? ''} · ${editing.source?.id ?? ''}` : undefined}>
  <form
    class="flex flex-col gap-6"
    onsubmit={(e) => {
      e.preventDefault();
      if (!disabled && !form.saving) form.run();
    }}
  >
    <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
      {#if !editing}
        <Field label="Provider" for="src-kind" class="sm:col-span-2">
          {#if providers.length <= 1}
            <div id="src-kind" class="input-static">{provider?.name ?? '–'}</div>
          {:else}
            <Select id="src-kind" bind:value={kindText} items={providers.map((p) => ({ value: String(p.kind), label: p.name }))} />
          {/if}
        </Field>
        <Field label="Id" for="src-id" required error={id && !idOk ? 'Letters, digits, dots, dashes, and underscores' : undefined}>
          <TextInput id="src-id" mono bind:value={id} empty={idHint} invalid={!!id && !idOk} />
        </Field>
      {/if}
      <Field label="Name" for="src-name" class={editing ? 'sm:col-span-2' : ''}>
        <TextInput id="src-name" bind:value={name} empty={id} />
      </Field>
    </div>

    {#each transports as t (t)}
      <div class="flex flex-col gap-3">
        {#if transports.length > 1}<h3 class="caps text-fg-faint">{transportName(t)}</h3>{/if}
        <ConfigForm fields={fields.filter((f) => f.transport === t)} bind:values={config} idPrefix="src-{t}" />
      </div>
    {/each}
    {#if fields.length === 0 && provider}
      <p class="text-sm text-fg-faint">This provider has no settings.</p>
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
      <span class="text-xs text-fg-faint">Built-in source. It can be edited but not removed.</span>
    {/if}
    {#if missing.length}<span class="text-sm text-warn">Required: {missing.join(', ')}</span>{/if}
    <span class="ml-auto flex gap-2">
      <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
      <Button variant="primary" loading={form.saving} {disabled} onclick={form.run}>{editing ? 'Save' : 'Add source'}</Button>
    </span>
  {/snippet}
</Dialog>
