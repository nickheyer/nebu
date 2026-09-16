<script lang="ts">
  import { ConfigType, type ConfigField } from '$proto/source_pb';
  import Field from '../ui/Field.svelte';
  import Select from '../ui/Select.svelte';
  import Switch from '../ui/Switch.svelte';
  import NumberInput from '../ui/NumberInput.svelte';
  import TextInput from '../ui/TextInput.svelte';
  import FilePicker from '../ui/FilePicker.svelte';

  // The settings a method takes, one under another: the default shows in the empty control and is sent as
  // nothing, a choice the host cannot take is listed greyed with the reason, a path is typed or picked on
  // the host, and a switch applies as it is flipped. Only values that differ from the default are kept.
  let { fields = [], values = $bindable({}), idPrefix = 'setting', class: cls = '' }: { fields?: ConfigField[]; values?: Record<string, string>; idPrefix?: string; class?: string } = $props();

  function set(name: string, v: string) {
    const next = { ...values };
    if (v === '') delete next[name];
    else next[name] = v;
    values = next;
  }

  const empty = (f: ConfigField) => f.default || f.placeholder;
  const label = (f: ConfigField) => f.label || f.name;
  const choiceLabel = (f: ConfigField, c: string) => f.choiceLabels[c] || c;

  // The default first, chosen as nothing, then every other choice
  function items(f: ConfigField) {
    const rest = f.choices.filter((c) => c !== f.default).map((c) => ({ value: c, label: choiceLabel(f, c), disabled: c in f.choiceUnmet, detail: f.choiceUnmet[c] }));
    return f.default ? [{ value: '', label: choiceLabel(f, f.default) }, ...rest] : rest;
  }
</script>

<div class="flex flex-col gap-4 {cls}">
  {#each fields as f (f.name)}
    {@const id = `${idPrefix}-${f.name}`}
    {#if f.type === ConfigType.BOOL}
      <div class="flex min-h-9 items-center justify-between gap-4">
        <span class="text-[13px] font-medium text-fg">{label(f)}</span>
        <Switch checked={(values[f.name] ?? f.default) === 'true'} label={label(f)} onchange={(on) => set(f.name, String(on) === f.default ? '' : String(on))} />
      </div>
    {:else}
      <Field label={label(f)} for={id} required={f.required && !f.default}>
        {#if f.choices.length}
          <Select {id} mono={!Object.keys(f.choiceLabels).length} value={values[f.name] ?? ''} empty={f.placeholder || 'Choose'} onchange={(v) => set(f.name, v)} items={items(f)} />
        {:else if f.type === ConfigType.PATH}
          <FilePicker {id} empty={f.default} bind:value={() => values[f.name] ?? '', (v) => set(f.name, v)} />
        {:else if f.type === ConfigType.INT}
          <NumberInput {id} integer empty={empty(f)} bind:value={() => values[f.name] ?? '', (v) => set(f.name, v)} />
        {:else}
          <TextInput {id} mono={f.type !== ConfigType.STRING} type={f.type === ConfigType.URL ? 'url' : 'text'} empty={empty(f)} bind:value={() => values[f.name] ?? '', (v) => set(f.name, v)} />
        {/if}
      </Field>
    {/if}
  {/each}
</div>
