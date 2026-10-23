<script lang="ts">
  import { live } from '$lib/state.svelte';
  import { byName, tail } from '$lib/format';
  import { weightsName } from '$lib/catalog';
  import { draftFiles, pathLabel, picksModel, storeRef } from '$lib/diffusion';
  import type { Param } from '$proto/runtime_pb';
  import type { StoredModel } from '$proto/store_pb';
  import Select from './ui/Select.svelte';

  let { value = $bindable(''), id, param, empty = '' }: { value?: string; id?: string; param: Param; empty?: string } = $props();

  // A draft pick lists each head file a group holds by its stored path, a group without one by reference
  function entries(m: StoredModel): { value: string; label: string }[] {
    const label = `${tail(m.repo)} · ${weightsName(m.group, m.formatId)}`;
    const heads = param.picks === 'draft' ? draftFiles(m) : [];
    if (!heads.length) return [{ value: storeRef(m), label }];
    return heads.map((a) => ({ value: a.path, label: `${label} · ${tail(a.artifact?.path || a.path)}` }));
  }

  // The daemon resolves each reference to the file the parameter loads from the group.
  const fitting = $derived([...live.models.values()].filter((m) => picksModel(m, param)).sort(byName((m) => m.repo + m.group)));
  const items = $derived.by(() => {
    // A resolved parameter needs an explicit way to load nothing, since an empty value means auto.
    const none = param.solved ? [{ value: 'none', label: 'None' }] : [];
    const out = [{ value: '', label: empty || '–' }, ...none, ...fitting.flatMap(entries)];
    // Preserve values set by hand or naming groups no longer in the store.
    if (value && !out.some((i) => i.value === value)) out.push({ value, label: pathLabel(value) });
    return out;
  });
</script>

<Select {id} mono unset {value} onchange={(next) => (value = next)} {items} disabled={items.length === 1} />
