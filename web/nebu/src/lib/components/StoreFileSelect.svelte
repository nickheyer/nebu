<script lang="ts">
  import { live } from '$lib/state.svelte';
  import { byName, tail } from '$lib/format';
  import { weightsName } from '$lib/catalog';
  import { storeRef } from '$lib/diffusion';
  import type { Param } from '$proto/runtime_pb';
  import type { StoredModel } from '$proto/store_pb';
  import Combo, { type ComboItem } from './ui/Combo.svelte';

  let { value = $bindable(''), id, param, empty = '' }: { value?: string; id?: string; param: Param; empty?: string } = $props();

  // A group by reference, which resolves to the file the parameter loads from it, then each of its files by path
  function entries(m: StoredModel): ComboItem[] {
    const group = `${tail(m.repo)} · ${weightsName(m.group, m.formatId)}`;
    const files = m.artifacts.map((a) => ({ value: a.path, label: tail(a.artifact?.path || a.path), group })).sort(byName((i) => i.label));
    return [{ value: storeRef(m), label: 'the group', detail: param.directory ? 'its directory' : 'the file it loads', group }, ...files];
  }
  const stored = $derived([...live.models.values()].sort(byName((m) => m.repo + m.group)));
  const items = $derived.by(() => {
    // A resolved parameter needs an explicit way to load nothing, since an empty value means auto
    const none: ComboItem[] = param.solved ? [{ value: 'none', label: 'none', detail: 'load nothing' }] : [];
    return [...none, ...stored.flatMap(entries)];
  });
</script>

<Combo {id} mono bind:value {items} {empty} />
