<script lang="ts">
  import { api } from '$lib/api';
  import { createForm } from '$lib/form.svelte';
  import { humanize } from '$lib/catalog';
  import { ConfigType, type ConfigField, type Provider, type SourceStatus } from '$proto/source_pb';
  import { SourceKind } from '$proto/source_pb';
  import FormDialog from './ui/FormDialog.svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Checkbox from './ui/Checkbox.svelte';
  import Section from './ui/Section.svelte';

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
  const missing = $derived(fields.filter((f) => f.required && !(config[f.name] ?? '').trim()).map((f) => f.label));
  const idOk = $derived(/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(id) && !id.includes('..'));

  // Switching providers starts the settings over, they belong to the provider
  $effect(() => {
    void kindText;
    if (!editing) config = {};
  });

  const acronyms = new Set(['http', 'cli', 'api', 'oci', 'ngc', 'ssh', 'lfs']);
  const transportName = (t: string) => (acronyms.has(t.toLowerCase()) ? t.toUpperCase() : humanize(t));

  function inputType(f: ConfigField): string {
    if (f.type === ConfigType.INT) return 'number';
    if (f.type === ConfigType.URL) return 'url';
    return 'text';
  }

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
      return { title: `Added ${source.name || source.id}`, link: { href: `/catalog?source=${source.id}`, label: 'Open' } };
    },
    failTitle: () => (editing ? 'Update failed' : 'Add failed')
  });
</script>

<FormDialog
  bind:open
  title={editing ? `Edit ${editing.source?.name || editing.source?.id}` : 'New source'}
  subtitle={editing ? `${editing.capabilities?.name ?? ''} · ${editing.source?.id ?? ''}` : undefined}
  size="lg"
  action={editing ? 'Save' : 'Add'}
  saving={form.saving}
  disabled={(!editing && (!idOk || !provider)) || missing.length > 0}
  note={missing.length ? `Needs ${missing.join(' and ')}` : ''}
  onsubmit={form.run}
>
  <div class="flex flex-col gap-6">
    <div class="grid grid-cols-1 gap-x-4 gap-y-5 sm:grid-cols-2">
      {#if !editing}
        <Field label="Provider" for="src-kind" info={provider?.description || undefined} class="sm:col-span-2">
          {#if providers.length <= 1}
            <div id="src-kind" class="input-static">{provider?.name ?? '–'}</div>
          {:else}
            <Select id="src-kind" bind:value={kindText} items={providers.map((p) => ({ value: String(p.kind), label: p.name }))} />
          {/if}
        </Field>
        <Field label="Id" for="src-id" info="Fixed once created" error={id && !idOk ? 'Letters, digits, dots, dashes, and underscores' : undefined}>
          <input id="src-id" class="input font-mono" bind:value={id} placeholder="my-source" aria-invalid={!!id && !idOk} autocomplete="off" spellcheck="false" />
        </Field>
      {/if}
      <Field label="Name" for="src-name" class={editing ? 'sm:col-span-2' : ''}>
        <input id="src-name" class="input" bind:value={name} placeholder={provider?.name} autocomplete="off" />
      </Field>
    </div>

    {#each transports as t (t)}
      <Section title={transports.length > 1 ? transportName(t) : 'Settings'}>
        <div class="grid grid-cols-1 gap-x-4 gap-y-5 sm:grid-cols-2">
          {#each fields.filter((f) => f.transport === t) as f (f.name)}
            {#if f.type === ConfigType.BOOL}
              <Checkbox class="sm:col-span-2" checked={(config[f.name] ?? f.default) === 'true'} onchange={(on) => (config = { ...config, [f.name]: on ? 'true' : 'false' })} label={f.label} info={f.description || undefined} />
            {:else}
              <Field label={f.label + (f.required ? ' *' : '')} for="src-{f.name}" info={f.description || undefined}>
                {#if f.choices.length}
                  <Select id="src-{f.name}" value={config[f.name] ?? ''} items={[{ value: '', label: f.default ? 'Default' : 'Not set', detail: f.default || undefined }, ...f.choices.map((c) => ({ value: c, label: c }))]} />
                {:else}
                  <input id="src-{f.name}" class="input {f.type === ConfigType.STRING ? '' : 'font-mono'}" type={inputType(f)} value={config[f.name] ?? ''} oninput={(e) => (config = { ...config, [f.name]: (e.currentTarget as HTMLInputElement).value })} placeholder={f.default || (f.type === ConfigType.PATH ? 'directory' : f.type === ConfigType.ENV ? 'VARIABLE_NAME' : f.type === ConfigType.URL ? 'https://host' : '')} autocomplete="off" spellcheck="false" />
                {/if}
              </Field>
            {/if}
          {/each}
        </div>
      </Section>
    {/each}
    {#if fields.length === 0 && provider}
      <p class="text-sm text-fg-faint">Nothing to configure</p>
    {/if}
  </div>
</FormDialog>
