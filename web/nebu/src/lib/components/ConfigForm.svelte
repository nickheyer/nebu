<script lang="ts">
  import { ConfigType, type ConfigField } from '$proto/source_pb';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import SwitchRow from './ui/SwitchRow.svelte';
  import NumberInput from './ui/NumberInput.svelte';
  import TextInput from './ui/TextInput.svelte';

  // Empty fields use defaults. Store only overrides. Required fields need a value or default.
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

  // Fallback placeholder when the daemon provides neither a default nor a format.
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
      <SwitchRow checked={(values[f.name] ?? f.default) === 'true'} label={f.label || f.name} description={f.description || undefined} onchange={(on) => set(f.name, on ? 'true' : 'false')} />
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
