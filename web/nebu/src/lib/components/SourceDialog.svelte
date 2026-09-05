<script lang="ts">
  import { api } from '$lib/api';
  import { createForm } from '$lib/form.svelte';
  import { humanize } from '$lib/catalog';
  import { ConfigType, type ConfigField, type Provider, type SourceStatus } from '$proto/source_pb';
  import { SourceKind } from '$proto/source_pb';
  import FormDialog from './ui/FormDialog.svelte';
  import Field from './ui/Field.svelte';
  import CheckCard from './ui/CheckCard.svelte';

  let { open = $bindable(false), providers = [], editing = null }: { open?: boolean; providers?: Provider[]; editing?: SourceStatus | null } = $props();

  let kind = $state<SourceKind>(SourceKind.UNSPECIFIED);
  let id = $state('');
  let name = $state('');
  let config = $state<Record<string, string>>({});

  const provider = $derived(providers.find((p) => p.kind === kind));
  // The provider declares the form, an existing source carrying it in its capabilities
  const fields = $derived<ConfigField[]>(editing ? (editing.capabilities?.fields ?? []) : (provider?.fields ?? []));
  const transports = $derived([...new Set(fields.map((f) => f.transport))]);
  const missing = $derived(fields.filter((f) => f.required && !(config[f.name] ?? '').trim()).map((f) => f.label));
  const idOk = $derived(/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(id) && !id.includes('..'));

  // Switching providers starts the settings over, they belong to the provider
  function pick(k: SourceKind) {
    kind = k;
    config = {};
  }

  function inputType(f: ConfigField): string {
    if (f.type === ConfigType.INT) return 'number';
    if (f.type === ConfigType.URL) return 'url';
    return 'text';
  }

  const form = createForm({
    open: () => open,
    close: () => (open = false),
    reset() {
      kind = editing?.source?.kind ?? providers[0]?.kind ?? SourceKind.UNSPECIFIED;
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
</script>

<FormDialog
  bind:open
  title={editing ? `Edit ${editing.source?.name || editing.source?.id}` : 'Add a source'}
  description={editing ? `${editing.capabilities?.name ?? ''} · ${editing.source?.id ?? ''}` : undefined}
  size="lg"
  action={editing ? 'Save' : 'Add source'}
  saving={form.saving}
  disabled={(!editing && (!idOk || !provider)) || missing.length > 0}
  note={missing.length ? `Needs ${missing.join(', ')}` : ''}
  onsubmit={form.run}
>
  <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
    {#if !editing}
      <Field label="Provider" for="src-kind" hint={provider?.description} class="sm:col-span-2">
        <select id="src-kind" class="input" value={kind} onchange={(e) => pick(Number((e.currentTarget as HTMLSelectElement).value) as SourceKind)}>
          {#each providers as p (p.kind)}<option value={p.kind}>{p.name}</option>{/each}
        </select>
      </Field>
      <Field label="Id" for="src-id" hint="Cannot change later">
        <input id="src-id" class="input font-mono" bind:value={id} placeholder="my-source" aria-invalid={!!id && !idOk} />
      </Field>
    {/if}
    <Field label="Name" for="src-name" class={editing ? 'sm:col-span-2' : ''}>
      <input id="src-name" class="input" bind:value={name} placeholder={provider?.name} />
    </Field>

    {#each transports as t (t)}
      {#if transports.length > 1}
        <div class="mt-1 text-[10.5px] font-semibold tracking-wider text-fg-faint uppercase sm:col-span-2">{humanize(t)}</div>
      {/if}
      {#each fields.filter((f) => f.transport === t) as f (f.name)}
        {#if f.type === ConfigType.BOOL}
          <CheckCard class="sm:col-span-2" checked={(config[f.name] ?? f.default) === 'true'} onchange={(on) => (config = { ...config, [f.name]: on ? 'true' : 'false' })} title={f.label} description={f.description} />
        {:else}
          <Field label={f.label + (f.required ? ' *' : '')} for="src-{f.name}" hint={f.description}>
            {#if f.choices.length}
              <select id="src-{f.name}" class="input" value={config[f.name] ?? ''} onchange={(e) => (config = { ...config, [f.name]: (e.currentTarget as HTMLSelectElement).value })}>
                <option value="">{f.default ? `Default · ${f.default}` : 'Not set'}</option>
                {#each f.choices as c (c)}<option value={c}>{c}</option>{/each}
              </select>
            {:else}
              <input id="src-{f.name}" class="input {f.type === ConfigType.STRING ? '' : 'font-mono'}" type={inputType(f)} value={config[f.name] ?? ''} oninput={(e) => (config = { ...config, [f.name]: (e.currentTarget as HTMLInputElement).value })} placeholder={f.default || (f.type === ConfigType.PATH ? 'directory' : f.type === ConfigType.ENV ? 'VARIABLE_NAME' : f.type === ConfigType.URL ? 'https://host' : '')} autocomplete="off" spellcheck="false" />
            {/if}
          </Field>
        {/if}
      {/each}
    {/each}
    {#if fields.length === 0 && provider}
      <p class="text-sm text-fg-muted sm:col-span-2">Nothing to configure</p>
    {/if}
  </div>
</FormDialog>
