<script lang="ts">
  import { api, message } from '$lib/api';
  import { ConfigType, type ConfigField } from '$proto/source_pb';
  import Field from '../ui/Field.svelte';
  import Select from '../ui/Select.svelte';
  import Switch from '../ui/Switch.svelte';
  import NumberInput from '../ui/NumberInput.svelte';
  import TextInput from '../ui/TextInput.svelte';
  import FilePicker from '../ui/FilePicker.svelte';
  import Spinner from '../ui/Spinner.svelte';

  // The settings a method takes, one under another: the default shows in the empty control and is sent as
  // nothing, a choice the host cannot take is listed greyed with the reason, a path is typed or picked on
  // the host, and a switch applies as it is flipped. A field that takes a revision of a repository shows the
  // revision applying when empty once the source lists it. Only values that differ from the default are kept.
  let { fields = [], values = $bindable({}), idPrefix = 'setting', class: cls = '' }: { fields?: ConfigField[]; values?: Record<string, string>; idPrefix?: string; class?: string } = $props();

  function set(name: string, v: string) {
    const next = { ...values };
    if (v === '') delete next[name];
    else next[name] = v;
    values = next;
  }

  // The default revision of each repository a field names: absent until listed, then its name or the refusal
  const revKey = (f: ConfigField) => `${f.revisionSource}/${f.revisionRepo}`;
  const asked = new Set<string>();
  let revisions = $state<Record<string, { name: string; error: string }>>({});
  $effect(() => {
    for (const f of fields) {
      if (!f.revisionRepo || asked.has(revKey(f))) continue;
      const key = revKey(f);
      asked.add(key);
      api.sources
        .listRevisions({ sourceId: f.revisionSource, repo: f.revisionRepo })
        .then((r) => {
          const d = r.revisions.find((x) => x.default) ?? r.revisions[0];
          revisions[key] = d ? { name: d.name, error: '' } : { name: '', error: `${f.revisionRepo} lists no revisions` };
        })
        .catch((err) => {
          revisions[key] = { name: '', error: message(err) };
        });
    }
  });
  const revision = (f: ConfigField) => (f.revisionRepo ? revisions[revKey(f)] : undefined);
  const listing = (f: ConfigField) => !!f.revisionRepo && !revisions[revKey(f)];

  const empty = (f: ConfigField) => f.default || revision(f)?.name || f.placeholder;
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
      <Field label={label(f)} for={id} required={f.required && !f.default} error={revision(f)?.error || undefined}>
        {#if f.choices.length}
          <Select {id} mono={!Object.keys(f.choiceLabels).length} value={values[f.name] ?? ''} onchange={(v) => set(f.name, v)} items={items(f)} />
        {:else if f.type === ConfigType.PATH}
          <FilePicker {id} empty={f.default} bind:value={() => values[f.name] ?? '', (v) => set(f.name, v)} />
        {:else if f.type === ConfigType.INT}
          <NumberInput {id} integer empty={empty(f)} bind:value={() => values[f.name] ?? '', (v) => set(f.name, v)} />
        {:else}
          <div class="relative">
            <TextInput {id} mono={f.type !== ConfigType.STRING} type={f.type === ConfigType.URL ? 'url' : 'text'} empty={empty(f)} inputClass={listing(f) ? 'pr-9' : ''} bind:value={() => values[f.name] ?? '', (v) => set(f.name, v)} />
            {#if listing(f)}<Spinner size={13} class="pointer-events-none absolute top-1/2 right-3 -translate-y-1/2 text-fg-faint" />{/if}
          </div>
        {/if}
      </Field>
    {/if}
  {/each}
</div>
