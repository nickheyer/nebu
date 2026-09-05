<script lang="ts">
  import { ParamType, type Param } from '$proto/runtime_pb';
  import { Plus, X } from '@lucide/svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import IconButton from './ui/IconButton.svelte';

  // Every parameter a runtime manifest declares as a field, an empty one inheriting what lies beneath
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

<div class="grid grid-cols-1 gap-x-4 gap-y-4 sm:grid-cols-2">
  {#each params as p (p.name)}
    {@const v = values[p.name] ?? ''}
    {@const fid = `${idPrefix}-${p.name}`}
    {@const bad = !valid(p, v)}
    <Field label={p.name} for={fid} info={p.description || undefined} error={bad ? (numeric(p) ? (p.solved ? 'A number or auto' : 'A number') : 'true or false') : undefined}>
      {#if p.choices.length || p.type === ParamType.BOOL}
        <Select id={fid} mono value={v} items={[{ value: '', label: 'Inherit', detail: beneath(p) }, ...(p.choices.length ? p.choices : ['true', 'false']).map((c) => ({ value: c, label: c }))]} />
      {:else}
        <input
          id={fid}
          class="input font-mono"
          type="text"
          inputmode={p.type === ParamType.INT ? 'numeric' : p.type === ParamType.FLOAT ? 'decimal' : 'text'}
          value={v}
          placeholder={beneath(p)}
          aria-invalid={bad}
          autocomplete="off"
          spellcheck="false"
          oninput={(e) => set(p.name, (e.currentTarget as HTMLInputElement).value)}
        />
      {/if}
    </Field>
  {/each}

  {#each extra as k (k)}
    <Field label={k} for="{idPrefix}-{k}" info={params.length ? 'Not in the manifest' : undefined}>
      <div class="flex gap-1">
        <input id="{idPrefix}-{k}" class="input font-mono" value={values[k]} autocomplete="off" spellcheck="false" oninput={(e) => (values = { ...values, [k]: (e.currentTarget as HTMLInputElement).value })} />
        <IconButton icon={X} label="Remove" onclick={() => set(k, '')} />
      </div>
    </Field>
  {/each}

  {#if params.length === 0}
    <div class="flex items-end gap-1 sm:col-span-2">
      <Field label="Parameter" for="{idPrefix}-new-name" class="flex-1">
        <input id="{idPrefix}-new-name" class="input font-mono" bind:value={newName} placeholder="name" autocomplete="off" spellcheck="false" onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), add())} />
      </Field>
      <Field label="Value" for="{idPrefix}-new-value" class="flex-1">
        <input id="{idPrefix}-new-value" class="input font-mono" bind:value={newValue} placeholder="value" autocomplete="off" spellcheck="false" onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), add())} />
      </Field>
      <IconButton icon={Plus} label="Add" variant="secondary" disabled={!newName.trim()} onclick={add} />
    </div>
  {/if}
</div>
