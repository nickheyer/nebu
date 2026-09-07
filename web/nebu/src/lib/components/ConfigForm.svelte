<script lang="ts">
  import { ConfigType, type ConfigField } from '$proto/source_pb';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Checkbox from './ui/Checkbox.svelte';

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

  function inputType(f: ConfigField): string {
    if (f.type === ConfigType.INT) return 'number';
    if (f.type === ConfigType.URL) return 'url';
    return 'text';
  }

  function placeholder(f: ConfigField): string {
    if (f.default) return f.default;
    switch (f.type) {
      case ConfigType.PATH:
        return '/path';
      case ConfigType.ENV:
        return 'VARIABLE_NAME';
      case ConfigType.URL:
        return 'https://host';
      default:
        return '';
    }
  }
</script>

<div class="grid grid-cols-1 gap-x-4 gap-y-5 sm:grid-cols-2 {cls}">
  {#each fields as f (f.name)}
    {@const fid = `${idPrefix}-${f.name}`}
    {#if f.type === ConfigType.BOOL}
      <Checkbox class="sm:col-span-2" checked={(values[f.name] ?? f.default) === 'true'} onchange={(on) => set(f.name, on ? 'true' : 'false')} label={f.label || f.name} hint={f.description || undefined} />
    {:else}
      <Field label={(f.label || f.name) + (f.required && !f.default ? ' *' : '')} for={fid} hint={f.description || undefined}>
        {#if f.choices.length}
          <Select id={fid} mono value={values[f.name] ?? ''} onchange={(v) => set(f.name, v)} items={[{ value: '', label: f.default ? 'Default' : 'Not set', detail: f.default || undefined }, ...f.choices.map((c) => ({ value: c, label: c }))]} />
        {:else}
          <input id={fid} class="input {f.type === ConfigType.STRING ? '' : 'font-mono'}" type={inputType(f)} value={values[f.name] ?? ''} oninput={(e) => set(f.name, (e.currentTarget as HTMLInputElement).value)} placeholder={placeholder(f)} autocomplete="off" spellcheck="false" />
        {/if}
      </Field>
    {/if}
  {/each}
</div>
