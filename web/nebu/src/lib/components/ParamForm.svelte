<script lang="ts">
  import { ParamType, type Param } from '$proto/runtime_pb';
  import { ChevronRight, Plus, X } from '@lucide/svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import NumberInput from './ui/NumberInput.svelte';
  import IconButton from './ui/IconButton.svelte';
  import TextInput from './ui/TextInput.svelte';
  import TextArea from './ui/TextArea.svelte';

  // Manifest groups stay in reading order. Empty fields inherit the slot or runtime default.
  let {
    params = [],
    values = $bindable({}),
    invalid = $bindable(0),
    inherited = {},
    idPrefix = 'param'
  }: { params?: Param[]; values?: Record<string, string>; invalid?: number; inherited?: Record<string, string>; idPrefix?: string } = $props();

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
  const regularGroups = $derived(groups.map((g) => ({ ...g, params: g.params.filter((p) => !p.advanced) })).filter((g) => g.params.length));
  const advancedGroups = $derived(groups.map((g) => ({ ...g, params: g.params.filter((p) => p.advanced) })).filter((g) => g.params.length));
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
    return p.default;
  }
  function beneathLabel(p: Param): string {
    const v = inherited[p.name];
    if (v !== undefined && v !== '') return `Slot default: ${v}`;
    const b = beneath(p);
    return b ? `Default: ${b}` : 'Not set';
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
  {@const multiline = p.advanced && p.type === ParamType.STRING && !p.choices.length}
  <div class="param-row" class:wide={multiline}>
    <div class="min-w-0">
      <label for={fid} class="text-[13px] font-medium text-fg" title={p.flag || p.env || p.name}>{p.label || p.name}</label>
      {#if p.description}<p class="mt-1 text-xs leading-5 text-fg-muted wrap-anywhere">{p.description}</p>{/if}
    </div>
    <div class="min-w-0">
      {#if p.choices.length}
        <Select id={fid} mono value={v} onchange={(next) => set(p.name, next)} items={[{ value: '', label: beneathLabel(p) }, ...p.choices.filter((c) => c !== '' && c !== p.default).map((c) => ({ value: c, label: c }))]} />
      {:else if p.type === ParamType.BOOL}
        <Select id={fid} value={v} onchange={(next) => set(p.name, next)} items={[{ value: '', label: beneathLabel(p) }, { value: 'true', label: 'On' }, { value: 'false', label: 'Off' }]} />
      {:else if numeric(p)}
        <NumberInput id={fid} min={p.min || undefined} max={p.max || undefined} step={p.step || undefined} unit={p.unit || undefined} integer={p.type === ParamType.INT} empty={beneath(p) || 'not set'} invalid={!!err} bind:value={() => v, (next) => set(p.name, next)} />
      {:else if multiline}
        <TextArea id={fid} mono rows={3} empty={beneath(p) || 'Not set'} invalid={!!err} bind:value={() => v, (next) => set(p.name, next)} />
      {:else}
        <TextInput id={fid} mono empty={beneath(p) || 'Not set'} invalid={!!err} bind:value={() => v, (next) => set(p.name, next)} />
      {/if}
      {#if err}<p class="mt-1.5 text-xs text-bad">{err}</p>{/if}
    </div>
  </div>
{/snippet}

{#snippet groupList(list: { name: string; params: Param[] }[])}
  <div class="flex flex-col gap-5">
    {#each list as g (g.name)}
      <fieldset class="min-w-0">
        {#if g.name}<legend class="mb-1 w-full border-b border-line pb-2 text-xs font-medium text-fg-muted">{g.name}</legend>{/if}
        <div class="divide-y divide-line/50">
          {#each g.params as p (p.name)}{@render control(p)}{/each}
        </div>
      </fieldset>
    {/each}
  </div>
{/snippet}

<div class="param-form flex flex-col gap-5">
  {@render groupList(regularGroups)}

  {#if advancedCount}
    <div class="border-t border-line pt-3">
      <button type="button" class="flex w-full items-center gap-2 rounded-md py-1 text-left text-sm text-fg-muted transition-colors hover:text-fg" aria-expanded={showAdvanced} aria-controls="{idPrefix}-advanced" onclick={() => (showAdvanced = !showAdvanced)}>
        <ChevronRight size={14} class="shrink-0 transition-transform {showAdvanced ? 'rotate-90' : ''}" />
        Advanced parameters
        <span class="ml-auto text-xs tabular-nums text-fg-faint">{advancedCount}</span>
      </button>
      <div id="{idPrefix}-advanced" hidden={!showAdvanced}>
        {#if showAdvanced}<div class="pt-4">{@render groupList(advancedGroups)}</div>{/if}
      </div>
    </div>
  {/if}

  {#if extra.length}
    <fieldset class="min-w-0">
      <legend class="w-full border-b border-line pb-2 text-xs font-medium text-fg-muted">Additional parameters</legend>
      <div class="divide-y divide-line/50">
        {#each extra as k (k)}
          <div class="param-row">
            <div class="min-w-0">
              <label for="{idPrefix}-{k}" class="font-mono text-xs text-fg wrap-anywhere">{k}</label>
              <p class="mt-1 text-xs leading-5 text-fg-muted">The manifest no longer names this parameter.</p>
            </div>
            <div class="flex min-w-0 items-center gap-1">
              <TextInput id="{idPrefix}-{k}" class="flex-1" mono bind:value={() => values[k] ?? '', (next) => (values = { ...values, [k]: next })} />
              <IconButton icon={X} label="Remove {k}" onclick={() => set(k, '')} />
            </div>
          </div>
        {/each}
      </div>
    </fieldset>
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

<style>
  .param-form {
    container-type: inline-size;
  }

  .param-row {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 0.75rem;
    padding-block: 0.875rem;
  }

  @container (min-width: 34rem) {
    .param-row:not(.wide) {
      grid-template-columns: minmax(0, 1fr) 16rem;
      align-items: start;
      column-gap: 2rem;
    }
  }
</style>
