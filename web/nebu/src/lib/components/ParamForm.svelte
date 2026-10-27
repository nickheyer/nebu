<script lang="ts">
  import { ParamType, type Param } from '$proto/runtime_pb';
  import type { Part, ParamState } from '$proto/estimate_pb';
  import { ChevronRight, Plus, X } from '@lucide/svelte';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import NumberInput from './ui/NumberInput.svelte';
  import RangeInput from './ui/RangeInput.svelte';
  import IconButton from './ui/IconButton.svelte';
  import TextInput from './ui/TextInput.svelte';
  import TextArea from './ui/TextArea.svelte';
  import Combo from './ui/Combo.svelte';
  import StoreFileSelect from './StoreFileSelect.svelte';
  import PartsList from './PartsList.svelte';
  import { pathLabel } from '$lib/diffusion';

  let {
    params = [],
    values = $bindable({}),
    invalid = $bindable(0),
    inherited = {},
    states = [],
    solved = {},
    missing = [],
    idPrefix = 'param'
  }: { params?: Param[]; values?: Record<string, string>; invalid?: number; inherited?: Record<string, string>; states?: ParamState[]; solved?: Record<string, string>; missing?: Part[]; idPrefix?: string } = $props();

  let showAdvanced = $state(false);
  let newName = $state('');
  let newValue = $state('');

  // Preserve runtime group order, with ungrouped params first.
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
  // Keep unknown parameters editable after runtime changes.
  const known = $derived(new Set(params.map((p) => p.name)));
  const extra = $derived(Object.keys(values).filter((k) => !known.has(k)).sort());
  const stateOf = $derived(new Map(states.map((s) => [s.name, s])));
  const missingOf = $derived(new Map(missing.map((m) => [m.param, m])));
  // Always show required and explicitly set parameters.
  $effect(() => {
    if (params.some((p) => p.advanced && (values[p.name] || missingOf.has(p.name)))) showAdvanced = true;
  });

  function set(name: string, v: string) {
    const next = { ...values };
    if (v.trim() === '') delete next[name];
    else next[name] = v;
    values = next;
  }

  const isPath = (p: Param) => p.type === ParamType.PATH;

  // Default precedence: slot, plan, runtime.
  function beneath(p: Param): string {
    const v = inherited[p.name];
    if (v !== undefined && v !== '') return isPath(p) ? pathLabel(v) : v;
    if (p.solved) {
      const s = solved[p.name];
      if (s !== undefined && s !== '' && s.toLowerCase() !== 'auto') return isPath(p) ? pathLabel(s) : s;
      return 'auto';
    }
    return p.default;
  }

  // Planned bounds over runtime defaults, the range a slider spans and nothing more
  function bounds(p: Param): { min: number; max: number; step: number } {
    const s = stateOf.get(p.name);
    const min = s && (s.min !== 0 || s.max !== 0) ? s.min : p.min;
    const max = s && s.max !== 0 ? s.max : p.max;
    return { min, max, step: (s?.step || p.step) || 0 };
  }
  const ranged = (p: Param) => {
    const b = bounds(p);
    return numeric(p) && b.max !== 0 && b.max > b.min;
  };

  function numeric(p: Param): boolean {
    return p.type === ParamType.INT || p.type === ParamType.FLOAT;
  }

  // Only a value the parameter's type cannot carry is a problem, the runtime judges the rest
  function problem(p: Param, v: string | undefined): string {
    if (!v) return '';
    if (p.solved && v.toLowerCase() === 'auto') return '';
    if (p.type === ParamType.BOOL) return /^(true|false)$/i.test(v) ? '' : 'true or false';
    if (numeric(p)) {
      const ok = p.type === ParamType.INT ? /^-?\d+$/.test(v) : /^-?\d*\.?\d+([eE][-+]?\d+)?$/.test(v);
      if (!ok) return p.type === ParamType.INT ? 'A whole number' : 'A number';
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
  {@const b = bounds(p)}
  {@const part = missingOf.get(p.name)}
  <div class="param-row" class:wide={multiline}>
    <div class="flex min-h-9 min-w-0 flex-col justify-center">
      <label for={fid} class="text-[13px] leading-5 font-medium text-fg" title={p.flag || p.env || p.name}>{p.label || p.name}</label>
      {#if err}
        <p class="text-xs leading-4 text-bad" role="alert">{err}</p>
      {:else if p.description}
        <p class="font-mono text-xs leading-4 text-fg-faint wrap-anywhere">{p.description}</p>
      {/if}
    </div>
    <div class="min-w-0">
      {#if p.choices.length}
        <Combo id={fid} mono value={v} empty={beneath(p) || '–'} onchange={(next) => set(p.name, next)} items={p.choices.filter((c) => c !== '').map((c) => ({ value: c, label: c }))} />
      {:else if p.type === ParamType.BOOL}
        <Select id={fid} unset value={v} onchange={(next) => set(p.name, next)} items={[{ value: '', label: beneath(p) === 'true' ? 'On' : beneath(p) === 'false' ? 'Off' : '–' }, { value: 'true', label: 'On' }, { value: 'false', label: 'Off' }]} />
      {:else if ranged(p)}
        <RangeInput id={fid} min={b.min} max={b.max} step={b.step || (p.type === ParamType.INT ? 1 : 0.01)} unit={p.unit || undefined} integer={p.type === ParamType.INT} empty={beneath(p)} invalid={!!err} bind:value={() => v, (next) => set(p.name, next)} />
      {:else if numeric(p)}
        <NumberInput id={fid} min={b.min || undefined} max={b.max || undefined} step={b.step || undefined} unit={p.unit || undefined} integer={p.type === ParamType.INT} empty={beneath(p)} invalid={!!err} bind:value={() => v, (next) => set(p.name, next)} />
      {:else if isPath(p)}
        <StoreFileSelect id={fid} param={p} empty={beneath(p)} bind:value={() => v, (next) => set(p.name, next)} />
      {:else if multiline}
        <TextArea id={fid} mono rows={3} empty={beneath(p)} invalid={!!err} bind:value={() => v, (next) => set(p.name, next)} />
      {:else}
        <TextInput id={fid} mono empty={beneath(p)} invalid={!!err} bind:value={() => v, (next) => set(p.name, next)} />
      {/if}
      {#if part && !v}<div class="mt-2"><PartsList parts={[part]} compact /></div>{/if}
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
        Advanced
        <span class="ml-auto text-xs tabular-nums text-fg-faint">{advancedCount}</span>
      </button>
      <div id="{idPrefix}-advanced" hidden={!showAdvanced}>
        {#if showAdvanced}<div class="pt-4">{@render groupList(advancedGroups)}</div>{/if}
      </div>
    </div>
  {/if}

  {#if extra.length}
    <fieldset class="min-w-0">
      <legend class="w-full border-b border-line pb-2 text-xs font-medium text-fg-muted">Other</legend>
      <div class="divide-y divide-line/50">
        {#each extra as k (k)}
          <div class="param-row">
            <div class="min-w-0">
              <label for="{idPrefix}-{k}" class="font-mono text-xs text-fg wrap-anywhere">{k}</label>
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
        <TextInput id="{idPrefix}-new-name" mono bind:value={newName} onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), add())} />
      </Field>
      <Field label="Value" for="{idPrefix}-new-value" class="flex-1">
        <TextInput id="{idPrefix}-new-value" mono bind:value={newValue} onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), add())} />
      </Field>
      <IconButton icon={Plus} label="Add" variant="secondary" disabled={!newName.trim()} onclick={add} />
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
    gap: 0.5rem;
    padding-block: 0.625rem;
  }

  @container (min-width: 40rem) {
    .param-row:not(.wide) {
      grid-template-columns: minmax(0, 1fr) 22rem;
      align-items: center;
      column-gap: 2rem;
    }
  }
</style>
