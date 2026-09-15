<script lang="ts">
  import { ConfigType, type ConfigField } from '$proto/source_pb';
  import { InstallKind, type InstallOption, type Runtime } from '$proto/runtime_pb';
  import type { Component } from 'svelte';
  import { Download, FolderInput, Hammer } from '@lucide/svelte';
  import { install, verb } from '$lib/runtimes';
  import { fail } from '$lib/toast.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Segmented from '$lib/components/ui/Segmented.svelte';
  import NumberInput from '$lib/components/ui/NumberInput.svelte';
  import TextInput from '$lib/components/ui/TextInput.svelte';
  import Tip from '$lib/components/ui/Tip.svelte';

  // Every way this host can get the runtime, laid out on the page: a section per method, a row per
  // published build or recipe variant with its own button, and the few settings a method takes in a
  // strip above its rows
  let { runtime, options }: { runtime: Runtime; options: InstallOption[] } = $props();

  interface Row {
    value: string;
    label: string;
    mono: boolean;
    detail: string;
    // Why this host cannot take the row, empty when it can
    unmet: string;
  }

  interface Plan {
    id: string;
    kind: InstallKind;
    verb: string;
    description: string;
    unmet: string[];
    // The field whose choices are the rows: the published build, or the recipe variant
    rowField?: ConfigField;
    rows: Row[];
    // The binary of an adopted install, typed or found on PATH
    path?: ConfigField;
    // The settings shown above the rows
    strip: ConfigField[];
  }

  const icons: Record<InstallKind, Component<any>> = { [InstallKind.UNSPECIFIED]: Download, [InstallKind.ADOPTED]: FolderInput, [InstallKind.PREBUILT]: Download, [InstallKind.BUILT]: Hammer };

  const plans = $derived<Plan[]>(
    options.flatMap((o) => {
      if (!o.method) return [];
      const variants = new Map((o.recipe?.recipe?.variants ?? []).map((v) => [v.id, v]));
      const rowField = o.fields.find((f) => f.required && f.choices.length > 0);
      const path = o.method.kind === InstallKind.ADOPTED ? o.fields.find((f) => f.type === ConfigType.PATH) : undefined;
      const labeled = rowField ? Object.keys(rowField.choiceLabels).length > 0 : false;
      // A recipe without variants builds as one row named for the recipe
      const rows: Row[] = rowField
        ? rowField.choices.map((c) => ({ value: c, label: rowField.choiceLabels[c] ?? c, mono: !labeled, detail: variants.get(c)?.description ?? '', unmet: rowField.choiceUnmet[c] ?? '' }))
        : o.method.kind === InstallKind.BUILT && o.fields.length > 0
          ? [{ value: '', label: o.recipe?.recipe?.id || o.method.recipeId, mono: true, detail: o.recipe?.recipe?.description ?? '', unmet: '' }]
          : [];
      return [{ id: o.method.id, kind: o.method.kind, verb: verb[o.method.kind], description: o.method.description, unmet: o.unmet, rowField, rows, path, strip: o.fields.filter((f) => f !== rowField && f !== path) }];
    })
  );

  // Settings typed into each method's strip, keyed by method id; a field left out takes its default
  let values = $state<Record<string, Record<string, string>>>({});
  // The row whose install was just asked for, until the daemon answers
  let pending = $state('');

  const valueOf = (p: Plan, f: ConfigField) => values[p.id]?.[f.name] ?? f.default;
  const named = (f: ConfigField) => f.label || f.name;
  function set(p: Plan, f: ConfigField, v: string) {
    values = { ...values, [p.id]: { ...values[p.id], [f.name]: v } };
  }

  // The image is read for the container sandbox alone, so it stays out of the strip while the host builds
  function shown(p: Plan): ConfigField[] {
    const sandbox = p.strip.find((f) => f.name === 'sandbox');
    return p.strip.filter((f) => f.name !== 'image' || (sandbox !== undefined && valueOf(p, sandbox) === 'oci'));
  }
  // What keeps every row of a method from running: a required setting left empty, or a setting on a
  // choice this host cannot take
  function blocked(p: Plan): string {
    for (const f of shown(p)) {
      const v = valueOf(p, f);
      if (f.required && !v) return `${named(f)} is required`;
      if (f.choices.length > 0 && v in f.choiceUnmet) return f.choiceUnmet[v];
    }
    return '';
  }

  async function run(p: Plan, key: string, extra: Record<string, string>) {
    const settings: Record<string, string> = {};
    for (const f of shown(p)) {
      const v = values[p.id]?.[f.name];
      if (v !== undefined && (v !== '' || !f.default)) settings[f.name] = v;
    }
    Object.assign(settings, extra);
    pending = key;
    try {
      await install(runtime, p.id, settings);
    } catch (err) {
      fail(err, 'Install refused');
    } finally {
      pending = '';
    }
  }
</script>

{#snippet setting(p: Plan, f: ConfigField)}
  {@const v = valueOf(p, f)}
  {#if f.type === ConfigType.BOOL}
    <label class="flex cursor-pointer items-center gap-2 text-xs text-fg-faint" title={f.description}>
      <input type="checkbox" class="checkbox" checked={v === 'true'} onchange={(e) => set(p, f, e.currentTarget.checked ? 'true' : 'false')} />
      {named(f)}
    </label>
  {:else}
    <div class="flex items-center gap-2">
      <span class="text-xs whitespace-nowrap text-fg-faint" title={f.description}>{named(f)}</span>
      {#if f.choices.length}
        <Segmented size="sm" bind:value={() => v, (next) => set(p, f, next)} tabs={f.choices.map((c) => ({ id: c, label: f.choiceLabels[c] ?? c, unmet: f.choiceUnmet[c] }))} />
      {:else if f.type === ConfigType.INT}
        <NumberInput size="sm" integer empty={f.default || f.placeholder} class="w-32" bind:value={() => values[p.id]?.[f.name] ?? '', (next) => set(p, f, next)} />
      {:else}
        <TextInput size="sm" mono class="w-52" aria-label={named(f)} title={f.description} empty={f.default || f.placeholder} bind:value={() => values[p.id]?.[f.name] ?? '', (next) => set(p, f, next)} />
      {/if}
    </div>
  {/if}
{/snippet}

<div class="flex flex-col gap-8">
  {#each plans as p (p.id)}
    {@const stop = blocked(p)}
    <Section title={p.verb} meta={p.description}>
      {#if p.unmet.length}<div class="note note-warn">{p.unmet.join(' · ')}</div>{/if}
      {#if p.path}
        {@const path = valueOf(p, p.path)}
        <div class="flex items-center gap-2">
          <TextInput mono class="max-w-2xl flex-1" aria-label={named(p.path)} title={p.path.description} empty={p.path.placeholder} bind:value={() => path, (next) => set(p, p.path!, next)} />
          <Button icon={icons[p.kind]} loading={pending === p.id} disabled={!path.trim()} onclick={() => run(p, p.id, { [p.path!.name]: path.trim() })}>{p.verb}</Button>
        </div>
      {/if}
      {#if shown(p).length}
        <div class="flex flex-wrap items-center gap-x-6 gap-y-2">
          {#each shown(p) as f (f.name)}{@render setting(p, f)}{/each}
        </div>
      {/if}
      {#if p.rows.length}
        <div class="overflow-x-auto">
          <table class="tbl">
            <thead><tr><th>{p.rowField ? named(p.rowField) : 'Recipe'}</th><th></th><th></th></tr></thead>
            <tbody>
              {#each p.rows as r (r.value)}
                {@const key = `${p.id}:${r.value}`}
                <tr>
                  <td class="w-px whitespace-nowrap text-fg {r.mono ? 'font-mono' : ''}">{r.label}</td>
                  <td class="text-fg-muted">{r.detail}</td>
                  <td class="actions">
                    <span>
                      {#if r.unmet || stop}
                        <Tip text={r.unmet || stop}><Button size="sm" icon={icons[p.kind]} disabled>{p.verb}</Button></Tip>
                      {:else}
                        <Button size="sm" icon={icons[p.kind]} loading={pending === key} disabled={pending !== ''} onclick={() => run(p, key, p.rowField ? { [p.rowField.name]: r.value } : {})}>{p.verb}</Button>
                      {/if}
                    </span>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </Section>
  {/each}
</div>
