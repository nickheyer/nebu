<script lang="ts">
  import { ParamType, type Param } from '$proto/runtime_pb';
  import { Plus, X } from '@lucide/svelte';
  import Field from './ui/Field.svelte';

  let {
    params = [],
    values = $bindable({}),
    invalid = $bindable(0),
    inherited = {},
    idPrefix = 'param'
  }: { params?: Param[]; values?: Record<string, string>; invalid?: number; inherited?: Record<string, string>; idPrefix?: string } = $props();

  let newName = $state('');
  let newValue = $state('');

  // Values the manifest does not name stay editable so nothing is lost when a manifest changes
  const known = $derived(new Set(params.map((p) => p.name)));
  const extra = $derived(Object.keys(values).filter((k) => !known.has(k)).sort());

  function set(name: string, v: string) {
    const next = { ...values };
    if (v.trim() === '') delete next[name];
    else next[name] = v;
    values = next;
  }

  // What applies when the field is left empty: the layer beneath, else the manifest default
  function beneath(p: Param): string {
    const v = inherited[p.name];
    if (v !== undefined && v !== '') return v;
    if (p.default.includes('{{')) return 'set at launch';
    return p.default || 'unset';
  }

  function numeric(p: Param): boolean {
    return p.type === ParamType.INT || p.type === ParamType.FLOAT;
  }

  function valid(p: Param, v: string | undefined): boolean {
    if (!v) return true;
    if (p.solved && v.toLowerCase() === 'auto') return true;
    if (p.choices.length) return p.choices.includes(v);
    if (p.type === ParamType.INT) return /^-?\d+$/.test(v);
    if (p.type === ParamType.FLOAT) return /^-?\d*\.?\d+([eE][-+]?\d+)?$/.test(v);
    if (p.type === ParamType.BOOL) return /^(true|false)$/i.test(v);
    return true;
  }

  // How many fields hold a value the runtime would refuse, so a dialog can hold its submit
  $effect(() => {
    invalid = params.filter((p) => !valid(p, values[p.name])).length;
  });

  function add() {
    const name = newName.trim();
    if (!name) return;
    set(name, newValue);
    newName = newValue = '';
  }
</script>

<div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
  {#each params as p (p.name)}
    {@const v = values[p.name] ?? ''}
    {@const fid = `${idPrefix}-${p.name}`}
    <Field label={p.name} for={fid} hint={p.description}>
      {#if p.choices.length || p.type === ParamType.BOOL}
        <select id={fid} class="input font-mono" value={v} onchange={(e) => set(p.name, (e.currentTarget as HTMLSelectElement).value)}>
          <option value="">Inherit · {beneath(p)}</option>
          {#each p.choices.length ? p.choices : ['true', 'false'] as c (c)}<option value={c}>{c}</option>{/each}
        </select>
      {:else}
        <input
          id={fid}
          class="input font-mono"
          type="text"
          inputmode={p.type === ParamType.INT ? 'numeric' : p.type === ParamType.FLOAT ? 'decimal' : 'text'}
          value={v}
          placeholder={beneath(p)}
          aria-invalid={!valid(p, v)}
          autocomplete="off"
          spellcheck="false"
          oninput={(e) => set(p.name, (e.currentTarget as HTMLInputElement).value)}
        />
        {#if !valid(p, v)}<span class="text-xs text-bad">{numeric(p) ? (p.solved ? 'A number or auto' : 'A number') : 'true or false'}</span>{/if}
      {/if}
    </Field>
  {/each}

  {#each extra as k (k)}
    <Field label={k} for="{idPrefix}-{k}" hint={params.length ? 'Unknown to this runtime' : ''}>
      <div class="flex gap-1.5">
        <input id="{idPrefix}-{k}" class="input font-mono" value={values[k]} autocomplete="off" spellcheck="false" oninput={(e) => (values = { ...values, [k]: (e.currentTarget as HTMLInputElement).value })} />
        <button type="button" class="shrink-0 rounded-md px-2 text-fg-muted hover:bg-raised hover:text-fg" aria-label="Remove {k}" onclick={() => set(k, '')}><X size={14} /></button>
      </div>
    </Field>
  {/each}

  {#if params.length === 0}
    <div class="flex items-end gap-1.5 sm:col-span-2">
      <Field label="Parameter" for="{idPrefix}-new-name" class="flex-1">
        <input id="{idPrefix}-new-name" class="input font-mono" bind:value={newName} placeholder="name" autocomplete="off" spellcheck="false" onkeydown={(e) => e.key === 'Enter' && add()} />
      </Field>
      <Field label="Value" for="{idPrefix}-new-value" class="flex-1">
        <input id="{idPrefix}-new-value" class="input font-mono" bind:value={newValue} placeholder="value" autocomplete="off" spellcheck="false" onkeydown={(e) => e.key === 'Enter' && add()} />
      </Field>
      <button type="button" class="input flex w-10 shrink-0 items-center justify-center text-fg-muted hover:text-fg" aria-label="Add parameter" disabled={!newName.trim()} onclick={add}><Plus size={14} /></button>
    </div>
  {/if}
</div>
