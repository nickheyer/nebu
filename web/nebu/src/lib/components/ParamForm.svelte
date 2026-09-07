<script lang="ts">
  import { ParamType, type Param } from '$proto/runtime_pb';
  import { ChevronRight, Plus, X } from '@lucide/svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import NumberInput from './ui/NumberInput.svelte';
  import IconButton from './ui/IconButton.svelte';
  import TextInput from './ui/TextInput.svelte';

  // Every parameter a runtime manifest declares, grouped the way the manifest groups them
  //
  // An empty field keeps what lies beneath: the slot's default, else the manifest default.
  let {
    params = [],
    values = $bindable({}),
    invalid = $bindable(0),
    inherited = {},
    idPrefix = 'param',
    columns = 2
  }: { params?: Param[]; values?: Record<string, string>; invalid?: number; inherited?: Record<string, string>; idPrefix?: string; columns?: 1 | 2 } = $props();

  let showAdvanced = $state(false);
  let newName = $state('');
  let newValue = $state('');

  // Groups in the order the manifest first names them, ungrouped params first
  const groups = $derived.by(() => {
    const out: { name: string; params: Param[] }[] = [];
    for (const p of params) {
      let g = out.find((x) => x.name === p.group);
      if (!g) out.push((g = { name: p.group, params: [] }));
      g.params.push(p);
    }
    return out.sort((a, b) => Number(!!a.name) - Number(!!b.name));
  });
  const advancedCount = $derived(params.filter((p) => p.advanced).length);
  // Values the manifest does not name stay editable so nothing is lost when a manifest changes
  const known = $derived(new Set(params.map((p) => p.name)));
  const extra = $derived(Object.keys(values).filter((k) => !known.has(k)).sort());
  // An advanced param with a value is worth seeing
  $effect(() => {
    if (params.some((p) => p.advanced && values[p.name])) showAdvanced = true;
  });

  function set(name: string, v: string) {
    const next = { ...values };
    if (v.trim() === '') delete next[name];
    else next[name] = v;
    values = next;
  }

  // What applies when the field is left empty
  function beneath(p: Param): string {
    const v = inherited[p.name];
    if (v !== undefined && v !== '') return v;
    if (p.solved) return 'auto';
    if (p.default.includes('{{')) return 'set at launch';
    return p.default || 'unset';
  }
  function beneathLabel(p: Param): string {
    const v = inherited[p.name];
    if (v !== undefined && v !== '') return `Slot default: ${v}`;
    return `Default: ${beneath(p)}`;
  }

  function numeric(p: Param): boolean {
    return p.type === ParamType.INT || p.type === ParamType.FLOAT;
  }

  function problem(p: Param, v: string | undefined): string {
    if (!v) return '';
    if (p.solved && v.toLowerCase() === 'auto') return '';
    if (p.choices.length) return p.choices.includes(v) ? '' : `One of ${p.choices.filter(Boolean).join(', ')}`;
    if (p.type === ParamType.BOOL) return /^(true|false)$/i.test(v) ? '' : 'true or false';
    if (numeric(p)) {
      const ok = p.type === ParamType.INT ? /^-?\d+$/.test(v) : /^-?\d*\.?\d+([eE][-+]?\d+)?$/.test(v);
      if (!ok) return p.type === ParamType.INT ? 'A whole number' : 'A number';
      const n = parseFloat(v);
      if (p.min !== 0 || p.max !== 0) {
        if (n < p.min) return `At least ${p.min}`;
        if (p.max !== 0 && n > p.max) return `At most ${p.max}`;
      }
    }
    return '';
  }

  $effect(() => {
    invalid = params.filter((p) => problem(p, values[p.name])).length;
  });

  function add() {
    const name = newName.trim();
    if (!name) return;
    set(name, newValue);
    newName = newValue = '';
  }
</script>

{#snippet control(p: Param)}
  {@const v = values[p.name] ?? ''}
  {@const fid = `${idPrefix}-${p.name}`}
  {@const err = problem(p, v)}
  <Field label={p.label || p.name} for={fid} description={p.description || undefined} error={err || undefined}>
    {#snippet trailing()}<span class="font-mono text-[11px] text-fg-faint">{p.flag || p.env || p.name}</span>{/snippet}
    {#if p.choices.length}
      <Select id={fid} mono value={v} onchange={(next) => set(p.name, next)} items={[{ value: '', label: beneathLabel(p) }, ...p.choices.filter((c) => c !== '' && c !== p.default).map((c) => ({ value: c, label: c }))]} />
    {:else if p.type === ParamType.BOOL}
      <Select id={fid} value={v} onchange={(next) => set(p.name, next)} items={[{ value: '', label: beneathLabel(p) }, { value: 'true', label: 'On' }, { value: 'false', label: 'Off' }]} />
    {:else if numeric(p)}
      <NumberInput id={fid} min={p.min || undefined} max={p.max || undefined} step={p.step || undefined} unit={p.unit || undefined} integer={p.type === ParamType.INT} empty={beneath(p)} invalid={!!err} bind:value={() => v, (next) => set(p.name, next)} />
    {:else}
      <TextInput id={fid} mono empty={beneath(p)} invalid={!!err} bind:value={() => v, (next) => set(p.name, next)} />
    {/if}
  </Field>
{/snippet}

<div class="flex flex-col gap-6">
  {#each groups as g (g.name)}
    {@const shown = g.params.filter((p) => !p.advanced || showAdvanced)}
    {#if shown.length}
      <div class="flex flex-col gap-3">
        {#if g.name}<h3 class="caps text-fg-faint">{g.name}</h3>{/if}
        <div class="grid grid-cols-1 gap-x-5 gap-y-4 {columns === 2 ? 'sm:grid-cols-2' : ''}">
          {#each shown as p (p.name)}{@render control(p)}{/each}
        </div>
      </div>
    {/if}
  {/each}

  {#if advancedCount}
    <button type="button" class="inline-flex w-fit items-center gap-1.5 text-sm text-fg-muted transition-colors hover:text-fg" aria-expanded={showAdvanced} onclick={() => (showAdvanced = !showAdvanced)}>
      <ChevronRight size={14} class="transition-transform {showAdvanced ? 'rotate-90' : ''}" />
      {showAdvanced ? 'Hide' : 'Show'} {advancedCount} advanced
    </button>
  {/if}

  {#if extra.length}
    <div class="flex flex-col gap-3">
      <h3 class="caps text-fg-faint">Not in the manifest</h3>
      <div class="grid grid-cols-1 gap-x-5 gap-y-4 {columns === 2 ? 'sm:grid-cols-2' : ''}">
        {#each extra as k (k)}
          <Field label={k} for="{idPrefix}-{k}" description="The manifest no longer names this parameter.">
            <div class="flex gap-1">
              <TextInput id="{idPrefix}-{k}" mono bind:value={() => values[k] ?? '', (next) => (values = { ...values, [k]: next })} />
              <IconButton icon={X} label="Remove" onclick={() => set(k, '')} />
            </div>
          </Field>
        {/each}
      </div>
    </div>
  {/if}

  {#if params.length === 0}
    <div class="flex items-end gap-2">
      <Field label="Parameter" for="{idPrefix}-new-name" class="flex-1">
        <TextInput id="{idPrefix}-new-name" mono bind:value={newName} empty="name" onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), add())} />
      </Field>
      <Field label="Value" for="{idPrefix}-new-value" class="flex-1">
        <TextInput id="{idPrefix}-new-value" mono bind:value={newValue} empty="value" onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), add())} />
      </Field>
      <IconButton icon={Plus} label="Add" variant="secondary" size="lg" disabled={!newName.trim()} onclick={add} />
    </div>
  {/if}
</div>
