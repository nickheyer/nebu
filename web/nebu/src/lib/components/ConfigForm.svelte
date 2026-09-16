<script lang="ts">
  import { ConfigType, type ConfigField } from '$proto/source_pb';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import Switch from './ui/Switch.svelte';
  import NumberInput from './ui/NumberInput.svelte';
  import TextInput from './ui/TextInput.svelte';

  // Fields the daemon describes, rendered the same way whatever they configure: a source, an install
  //
  // An empty value means the default. Only values that differ from the default are kept. The form is
  // valid once every required field holds a value of its own or a default; the grid takes as many
  // columns as the fields fill.
  let {
    fields = [],
    values = $bindable({}),
    valid = $bindable(true),
    idPrefix = 'cfg',
    class: cls = ''
  }: { fields?: ConfigField[]; values?: Record<string, string>; valid?: boolean; idPrefix?: string; class?: string } = $props();

  function set(name: string, v: string) {
    const next = { ...values };
    if (v === '') delete next[name];
    else next[name] = v;
    values = next;
  }

  // The shape a field's type takes, shown while empty and the daemon names neither a default nor a shape
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

  const missing = $derived(fields.filter((f) => f.required && !(values[f.name] ?? '').trim() && !f.default));
  $effect(() => {
    valid = missing.length === 0;
  });
</script>

<div class="grid gap-x-5 gap-y-4 {cls}" style="grid-template-columns: repeat(auto-fit, minmax(min(100%, 16rem), 1fr))">
  {#each fields as f (f.name)}
    {@const fid = `${idPrefix}-${f.name}`}
    {#if f.type === ConfigType.BOOL}
      <div class="flex items-start justify-between gap-4 rounded-md border border-line px-3 py-2.5">
        <div class="min-w-0">
          <label for={fid} class="text-[13px] font-medium text-fg">{f.label || f.name}</label>
          {#if f.description}<p class="text-xs leading-5 text-fg-muted">{f.description}</p>{/if}
        </div>
        <Switch checked={(values[f.name] ?? f.default) === 'true'} label={f.label || f.name} onchange={(on) => set(f.name, on ? 'true' : 'false')} />
      </div>
    {:else}
      <Field label={f.label || f.name} for={fid} required={f.required && !f.default} description={f.description || undefined} error={missing.includes(f) && (values[f.name] ?? '') !== '' ? undefined : undefined}>
        {#if f.choices.length}
          <Select id={fid} mono value={values[f.name] ?? ''} onchange={(v) => set(f.name, v)} items={[...(f.default ? [{ value: '', label: f.default }] : []), ...f.choices.filter((c) => c !== f.default || !f.default).map((c) => ({ value: c, label: c }))]} />
        {:else if f.type === ConfigType.INT}
          <NumberInput id={fid} integer empty={f.default || f.placeholder} bind:value={() => values[f.name] ?? '', (v) => set(f.name, v)} />
        {:else}
          <TextInput id={fid} mono={f.type !== ConfigType.STRING} type={f.type === ConfigType.URL ? 'url' : 'text'} empty={f.default || f.placeholder || hintFor(f)} bind:value={() => values[f.name] ?? '', (v) => set(f.name, v)} />
        {/if}
      </Field>
    {/if}
  {/each}
</div>
