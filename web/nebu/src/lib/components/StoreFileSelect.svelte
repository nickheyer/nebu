<script lang="ts">
  import { live } from '$lib/state.svelte';
  import { byName, tail } from '$lib/format';
  import { weightsName } from '$lib/catalog';
  import { pathLabel, picksModel, storeRef } from '$lib/diffusion';
  import type { Param } from '$proto/runtime_pb';
  import Select from './ui/Select.svelte';

  let { value = $bindable(''), id, param, empty = '' }: { value?: string; id?: string; param: Param; empty?: string } = $props();

  // The daemon resolves each reference to the file the parameter loads from the group.
  const fitting = $derived([...live.models.values()].filter((m) => picksModel(m, param)).sort(byName((m) => m.repo + m.group)));
  const items = $derived.by(() => {
    const out = [{ value: '', label: empty || '–' }, ...fitting.map((m) => ({ value: storeRef(m), label: `${tail(m.repo)} · ${weightsName(m.group, m.formatId)}` }))];
    // Preserve values set by hand or naming groups no longer in the store.
    if (value && !out.some((i) => i.value === value)) out.push({ value, label: pathLabel(value) });
    return out;
  });
</script>

<Select {id} mono unset {value} onchange={(next) => (value = next)} {items} disabled={items.length === 1} />
