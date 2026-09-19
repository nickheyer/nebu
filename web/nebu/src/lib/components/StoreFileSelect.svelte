<script lang="ts">
  import { live } from '$lib/state.svelte';
  import { byName, tail } from '$lib/format';
  import { weightsName } from '$lib/catalog';
  import { pickedPath, picksModel } from '$lib/diffusion';
  import Select from './ui/Select.svelte';

  let { value = $bindable(''), id, picks = '', empty = '' }: { value?: string; id?: string; picks?: string; empty?: string } = $props();

  const fitting = $derived([...live.models.values()].filter((m) => picksModel(m, picks) && pickedPath(m, picks)).sort(byName((m) => m.repo + m.group)));
  const items = $derived.by(() => {
    const out = [{ value: '', label: empty || '–' }, ...fitting.map((m) => ({ value: pickedPath(m, picks), label: `${tail(m.repo)} · ${weightsName(m.group, m.formatId)}` }))];
    // Preserve selected paths that are no longer in the store.
    if (value && !out.some((i) => i.value === value)) out.push({ value, label: tail(value) });
    return out;
  });
</script>

<Select {id} mono unset {value} onchange={(next) => (value = next)} {items} disabled={items.length === 1} />
