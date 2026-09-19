<script lang="ts">
  import { page } from '$app/state';
  import { replaceState } from '$app/navigation';
  import { live, tracesOf } from '$lib/state.svelte';
  import { byName } from '$lib/format';
  import { TraceKind, type Trace } from '$proto/gateway_pb';
  import { Activity } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Select from '$lib/components/ui/Select.svelte';
  import Segmented from '$lib/components/ui/Segmented.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import TraceTable from '$lib/components/TraceTable.svelte';
  import TraceDetail from '$lib/components/TraceDetail.svelte';

  let route = $state('');
  let kind = $state('inference');
  let view = $state('all');
  let selected = $state(page.url.searchParams.get('id') ?? '');

  const kinds = [
    { value: 'inference', label: 'Inference' },
    { value: 'all', label: 'All kinds' },
    { value: String(TraceKind.CHAT), label: 'Chat' },
    { value: String(TraceKind.GENERATE), label: 'Generate' },
    { value: String(TraceKind.EMBED), label: 'Embed' },
    { value: String(TraceKind.COUNT), label: 'Token counts' },
    { value: String(TraceKind.OTHER), label: 'Other' }
  ];
  const routes = $derived([...new Set([...live.routes.keys(), ...[...live.traces.values()].map((t) => t.route).filter(Boolean)])].sort());
  const all = $derived(tracesOf(route).filter((t) => (kind === 'all' ? true : kind === 'inference' ? t.kind !== TraceKind.COUNT : t.kind === Number(kind))));
  const traces = $derived(view === 'errors' ? all.filter((t) => t.error || t.status >= 400) : view === 'live' ? all.filter((t) => !t.finishedAt) : all);
  const errors = $derived(all.filter((t) => t.error || t.status >= 400).length);
  const inFlight = $derived(all.filter((t) => !t.finishedAt).length);

  function select(t: Trace) {
    selected = selected === t.id ? '' : t.id;
    const url = new URL(page.url);
    if (selected) url.searchParams.set('id', selected);
    else url.searchParams.delete('id');
    replaceState(url, {});
  }
</script>

<PageHeader title="Requests">
  {#snippet below()}
    <div class="flex flex-wrap items-center gap-2">
      <Select class="w-48" bind:value={route} label="Model" items={[{ value: '', label: 'All models' }, ...routes.map((r) => ({ value: r, label: r }))]} mono />
      <Select class="w-40" bind:value={kind} label="Kind" items={kinds} />
      <Segmented bind:value={view} tabs={[{ id: 'all', label: 'All', count: all.length }, { id: 'live', label: 'In flight', count: inFlight || undefined }, { id: 'errors', label: 'Errors', count: errors || undefined }]} />
    </div>
  {/snippet}
</PageHeader>

{#if live.traces.size === 0}
  <Empty icon={Activity} title="No requests yet" />
{:else}
  <div class="grid grid-cols-1 gap-6 {selected ? 'xl:grid-cols-[minmax(0,1fr)_28rem]' : ''}">
    <TraceTable {traces} {selected} onSelect={select} compact={!!selected} />
    {#if selected}
      <Card title="Request" class="xl:sticky xl:top-8 xl:max-h-[calc(100vh-4rem)] xl:self-start xl:overflow-y-auto"><TraceDetail id={selected} /></Card>
    {/if}
  </div>
{/if}
