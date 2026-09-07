<script lang="ts">
  import { ConfigType, type ConfigField } from '$proto/source_pb';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Switch from './ui/Switch.svelte';
  import NumberInput from './ui/NumberInput.svelte';
  import TextInput from './ui/TextInput.svelte';

  // Fields the daemon describes, rendered the same way whatever they configure: a source, an install
  //
  // An empty value means the default. Only values that differ from the default are kept.
  let { fields = [], values = $bindable({}), idPrefix = 'cfg', class: cls = '' }: { fields?: ConfigField[]; values?: Record<string, string>; idPrefix?: string; class?: string } = $props();

  function set(name: string, v: string) {
    const next = { ...values };
    if (v === '') delete next[name];
    else next[name] = v;
    values = next;
  }

  // A hint of the shape a field takes, shown while empty and no default applies
  function hintFor(f: ConfigField): string {
    switch (f.type) {
      case ConfigType.PATH:
        return '/path/to/file';
      case ConfigType.ENV:
        return 'VARIABLE_NAME';
      case ConfigType.URL:
        return 'https://example.com';
      default:
        return '';
    }
  }
</script>

<div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2 {cls}">
  {#each fields as f (f.name)}
    {@const fid = `${idPrefix}-${f.name}`}
    {#if f.type === ConfigType.BOOL}
      <div class="flex items-start justify-between gap-4 rounded-md border border-line px-3 py-2.5 sm:col-span-2">
        <div class="min-w-0">
          <label for={fid} class="text-[13px] font-medium text-fg">{f.label || f.name}</label>
          {#if f.description}<p class="text-xs leading-5 text-fg-muted">{f.description}</p>{/if}
        </div>
        <Switch checked={(values[f.name] ?? f.default) === 'true'} label={f.label || f.name} onchange={(on) => set(f.name, on ? 'true' : 'false')} />
      </div>
    {:else}
      <Field label={f.label || f.name} for={fid} required={f.required && !f.default} description={f.description || undefined}>
        {#if f.choices.length}
          <Select id={fid} mono value={values[f.name] ?? ''} empty="Choose" onchange={(v) => set(f.name, v)} items={[...(f.default ? [{ value: '', label: `Default: ${f.default}` }] : []), ...f.choices.filter((c) => c !== f.default || !f.default).map((c) => ({ value: c, label: c }))]} />
        {:else if f.type === ConfigType.INT}
          <NumberInput id={fid} integer fallback={f.default} bind:value={() => values[f.name] ?? '', (v) => set(f.name, v)} />
        {:else}
          <TextInput id={fid} mono={f.type !== ConfigType.STRING} type={f.type === ConfigType.URL ? 'url' : 'text'} fallback={f.default} empty={f.default ? '' : hintFor(f)} bind:value={() => values[f.name] ?? '', (v) => set(f.name, v)} />
        {/if}
      </Field>
    {/if}
  {/each}
</div>
