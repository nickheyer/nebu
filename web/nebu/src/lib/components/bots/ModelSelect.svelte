<script lang="ts">
  import { live } from '$lib/state.svelte';
  import { routesFor, routeDetail, type ModelKind } from '$lib/bots';
  import Select, { type SelectItem } from '../ui/Select.svelte';

  let { value = $bindable(''), kind, id, disabled = false }: { value?: string; kind: ModelKind; id?: string; disabled?: boolean } = $props();

  const nouns: Record<ModelKind, string> = { chat: 'language', image: 'image', video: 'video' };
  const routes = $derived(routesFor(kind, live.routes.values()));
  const items = $derived.by((): SelectItem[] => {
    const out: SelectItem[] = [{ value: '', label: 'Auto', detail: `Single ready ${nouns[kind]} model` }];
    for (const r of routes) out.push({ value: r.name, label: r.name, detail: routeDetail(r) });
    if (value && !routes.some((r) => r.name === value)) out.push({ value, label: value, detail: 'Route not found' });
    return out;
  });
</script>

<Select {id} bind:value {items} {disabled} />
