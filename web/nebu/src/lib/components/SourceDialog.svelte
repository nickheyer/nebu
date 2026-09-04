<script lang="ts">
  import { api } from '$lib/api';
  import { fail, ok } from '$lib/toast.svelte';
  import { humanize } from '$lib/catalog';
  import { ConfigType, type ConfigField, type Provider, type SourceStatus } from '$proto/source_pb';
  import { SourceKind } from '$proto/source_pb';
  import Dialog from './ui/Dialog.svelte';
  import Field from './ui/Field.svelte';
  import Button from './ui/Button.svelte';

  let {
    open = $bindable(false),
    providers = [],
    editing = null,
    onDone
  }: { open?: boolean; providers?: Provider[]; editing?: SourceStatus | null; onDone?: () => void } = $props();

  let kind = $state<SourceKind>(SourceKind.UNSPECIFIED);
  let id = $state('');
  let name = $state('');
  let config = $state<Record<string, string>>({});
  let saving = $state(false);

  const provider = $derived(providers.find((p) => p.kind === kind));
  // The provider declares the form; an existing source carries the same declaration in its capabilities
  const fields = $derived<ConfigField[]>(editing ? (editing.capabilities?.fields ?? []) : (provider?.fields ?? []));
  const transports = $derived([...new Set(fields.map((f) => f.transport))]);
  const missing = $derived(fields.filter((f) => f.required && !(config[f.name] ?? '').trim()).map((f) => f.label));
  const idOk = $derived(/^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(id) && !id.includes('..'));

  $effect(() => {
    if (!open) return;
    if (editing) {
      kind = editing.source?.kind ?? SourceKind.UNSPECIFIED;
      id = editing.source?.id ?? '';
      name = editing.source?.name ?? '';
      config = { ...(editing.source?.config ?? {}) };
    } else {
      kind = providers[0]?.kind ?? SourceKind.UNSPECIFIED;
      id = '';
      name = '';
      config = {};
    }
  });

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

  async function submit() {
    saving = true;
    const settings: Record<string, string> = {};
    for (const [k, v] of Object.entries(config)) if (v.trim()) settings[k] = v.trim();
    const source = { id: id.trim(), kind, name: name.trim(), config: settings };
    try {
      if (editing) {
        await api.sources.updateSource({ source });
        ok(`Updated ${source.name || source.id}`, 'Every page follows the change');
      } else {
        await api.sources.createSource({ source });
        ok(`Added ${source.name || source.id}`, `${provider?.name ?? 'The provider'} is in the catalog now`);
      }
      open = false;
      onDone?.();
    } catch (err) {
      fail(err, editing ? 'Update failed' : 'Add failed');
    } finally {
      saving = false;
    }
  }
</script>

<Dialog bind:open title={editing ? `Edit ${editing.source?.name || editing.source?.id}` : 'Add a source'} description={editing ? `${editing.capabilities?.name ?? ''} · ${editing.source?.id ?? ''}` : 'A source is one configured instance of a provider'} size="lg">
  <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
    {#if !editing}
      <Field label="Provider" for="src-kind" hint={provider?.description} class="sm:col-span-2">
        <select id="src-kind" class="input" value={kind} onchange={(e) => pick(Number((e.currentTarget as HTMLSelectElement).value) as SourceKind)}>
          {#each providers as p (p.kind)}<option value={p.kind}>{p.name}</option>{/each}
        </select>
      </Field>
      <Field label="Id" for="src-id" hint="Letters, digits, dots, dashes, or underscores. Pulled models are kept under it, so it cannot change later">
        <input id="src-id" class="input font-mono" bind:value={id} placeholder="hf-mirror" aria-invalid={!!id && !idOk} />
      </Field>
    {/if}
    <Field label="Name" for="src-name" hint="What the catalog calls it, the id when empty" class={editing ? 'sm:col-span-2' : ''}>
      <input id="src-name" class="input" bind:value={name} placeholder={provider?.name} />
    </Field>

    {#each transports as t (t)}
      {#if transports.length > 1}
        <div class="mt-1 text-[10.5px] font-semibold tracking-wider text-fg-faint uppercase sm:col-span-2">{humanize(t)}</div>
      {/if}
      {#each fields.filter((f) => f.transport === t) as f (f.name)}
        {#if f.type === ConfigType.BOOL}
          <label class="flex cursor-pointer items-start gap-3 rounded-lg border border-line bg-sunken px-3 py-2.5 sm:col-span-2">
            <input type="checkbox" class="mt-0.5 accent-accent" checked={(config[f.name] ?? f.default) === 'true'} onchange={(e) => (config = { ...config, [f.name]: (e.currentTarget as HTMLInputElement).checked ? 'true' : 'false' })} />
            <span class="text-sm">
              <span class="font-medium text-fg">{f.label}</span>
              <span class="block text-xs leading-5 text-fg-muted">{f.description}</span>
            </span>
          </label>
        {:else}
          <Field label={f.label + (f.required ? ' *' : '')} for="src-{f.name}" hint={f.description + (f.default ? `. Empty means ${f.default}` : '')}>
            {#if f.choices.length}
              <select id="src-{f.name}" class="input" value={config[f.name] ?? ''} onchange={(e) => (config = { ...config, [f.name]: (e.currentTarget as HTMLSelectElement).value })}>
                <option value="">{f.default ? `Default · ${f.default}` : 'Not set'}</option>
                {#each f.choices as c (c)}<option value={c}>{c}</option>{/each}
              </select>
            {:else}
              <input id="src-{f.name}" class="input {f.type === ConfigType.STRING ? '' : 'font-mono'}" type={inputType(f)} value={config[f.name] ?? ''} oninput={(e) => (config = { ...config, [f.name]: (e.currentTarget as HTMLInputElement).value })} placeholder={f.default || (f.type === ConfigType.PATH ? '/srv/models' : f.type === ConfigType.ENV ? 'MY_TOKEN' : '')} autocomplete="off" spellcheck="false" />
            {/if}
          </Field>
        {/if}
      {/each}
    {/each}
    {#if fields.length === 0 && provider}
      <p class="text-sm text-fg-muted sm:col-span-2">{provider.name} has nothing to configure.</p>
    {/if}
  </div>

  {#snippet footer()}
    {#if missing.length}<span class="mr-auto text-xs text-warn">Needs {missing.join(', ')}</span>{/if}
    <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
    <Button variant="primary" loading={saving} onclick={submit} disabled={(!editing && (!idOk || !provider)) || missing.length > 0}>{editing ? 'Save' : 'Add source'}</Button>
  {/snippet}
</Dialog>
