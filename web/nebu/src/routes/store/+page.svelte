<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { page } from '$app/state';
  import { replaceState } from '$app/navigation';
  import { api } from '$lib/api';
  import { live, cached, clock, modelKey, instanceLive } from '$lib/state.svelte';
  import { runModel } from '$lib/slotActions.svelte';
  import { launch, slotOccupied } from '$lib/launch';
  import { ago, byName, bytes, count, params as fmtParams, when } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import type { StoredModel } from '$proto/store_pb';
  import { Boxes, Play, FolderOutput, ShieldCheck, Trash2, Search, Recycle, HardDrive, ArrowLeftRight, Compass, SlidersHorizontal } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import Meter from '$lib/components/ui/Meter.svelte';
  import Pill from '$lib/components/ui/Pill.svelte';
  import Dialog from '$lib/components/ui/Dialog.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import SortTh from '$lib/components/ui/SortTh.svelte';
  import SkeletonRows from '$lib/components/ui/SkeletonRows.svelte';
  import ModelsNav from '$lib/components/ModelsNav.svelte';
  import ModelDrawer from '$lib/components/ModelDrawer.svelte';
  import { TableSort } from '$lib/sort.svelte';

  // The stream carries the totals in the snapshot and after every change
  const status = $derived(live.store);
  let filter = $state('');
  let exportOpen = $state(false);
  let exportTarget = $state<StoredModel | null>(null);
  let exportDir = $state('');
  let exporting = $state(false);
  let collecting = $state(false);
  let sourceFilter = $state('');
  let selected = $state<StoredModel | null>(null);
  let drawerOpen = $state(false);
  let slotId = $state('');
  let filterInput: HTMLInputElement | undefined = $state();
  const sort = new TableSort('pulled');
  const loading = $derived(!live.ready && !live.error);

  const sourceIds = $derived([...new Set([...live.models.values()].map((m) => m.sourceId))].sort());
  const several = $derived(sourceIds.length > 1);
  const slots = $derived([...live.slots.values()].sort(byName((s) => s.name)));
  // Eviction takes the model used longest ago, a model never run counting from its pull
  const usedAt = (m: StoredModel) => m.usedAt ?? m.pulledAt;
  const models = $derived(
    sort.apply(
      [...live.models.values()].filter((m) => (!sourceFilter || m.sourceId === sourceFilter) && (!filter || `${m.repo} ${m.group} ${m.formatId} ${m.descriptor?.architecture ?? ''}`.toLowerCase().includes(filter.toLowerCase()))),
      (m, key) => {
        switch (key) {
          case 'model':
            return `${m.repo} ${m.group}`;
          case 'source':
            return m.sourceId;
          case 'format':
            return m.formatId;
          case 'params':
            return m.descriptor?.parameterCount ?? 0n;
          case 'size':
            return m.bytes;
          case 'used':
            return usedAt(m)?.seconds ?? 0n;
          default:
            return m.pulledAt?.seconds ?? 0n;
        }
      }
    )
  );

  // What the models add up to against what the blobs take: the difference is bytes two groups share
  const referenced = $derived([...live.models.values()].reduce((a, m) => a + m.bytes, 0n));
  const onDisk = $derived(status?.blobBytes ?? referenced);
  const shared = $derived(referenced > onDisk ? referenced - onDisk : 0n);
  // The filesystem the store sits on, so usage reads against a real ceiling when no cap is set
  const mount = $derived.by(() => {
    const p = status?.path ?? '';
    if (!p) return undefined;
    return [...(live.host?.storage ?? [])].filter((s) => p.startsWith(s.path)).sort((a, b) => b.path.length - a.path.length)[0];
  });
  const ceiling = $derived(status?.maxBytes || mount?.totalBytes || 0n);
  const diskLine = $derived.by(() => {
    const parts: string[] = [];
    if (status?.maxBytes) parts.push(`${bytes(status.maxBytes, 0)} cap`);
    else if (mount) parts.push(`${bytes(mount.freeBytes)} free on ${mount.path}`);
    if (shared) parts.push(`${bytes(shared)} shared between models`);
    if (status?.partials) parts.push(`${count(status.partials)} partial ${Number(status.partials) === 1 ? 'pull' : 'pulls'} holding ${bytes(status.partialBytes)}`);
    return parts.join(' · ');
  });

  const sourceName = (id: string) => live.sources.get(id)?.name || id;
  const capsOf = (id: string) => cached.sources.find((s) => s.source?.id === id)?.capabilities;

  function servingAs(m: StoredModel): string[] {
    return [...live.instances.values()].filter((i) => i.sourceId === m.sourceId && i.repo === m.repo && i.group === m.group && instanceLive(i)).map((i) => (i.slotId ? (live.slots.get(i.slotId)?.name ?? i.name) : i.name));
  }

  function openModel(m: StoredModel) {
    selected = m;
    drawerOpen = true;
    replaceState(`/store?model=${encodeURIComponent(modelKey(m))}`, {});
  }

  // A model named in the URL opens once the snapshot has it, and closing the panel drops it from the URL
  let wanted = page.url.searchParams.get('model') ?? '';
  $effect(() => {
    if (!wanted || !live.ready) return;
    const m = live.models.get(wanted);
    wanted = '';
    if (m) untrack(() => openModel(m));
  });
  $effect(() => {
    if (!drawerOpen && !wanted && page.url.searchParams.has('model')) replaceState('/store', {});
  });

  onMount(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === '/' && !e.metaKey && !e.ctrlKey && !['INPUT', 'TEXTAREA', 'SELECT'].includes(document.activeElement?.tagName ?? '')) {
        e.preventDefault();
        filterInput?.focus();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  });

  async function remove(m: StoredModel) {
    const yes = await confirm({ title: `Remove ${m.repo} ${m.group}?`, message: 'Blobs nothing else references are deleted.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      const r = await api.store.removeModel({ sourceId: m.sourceId, repo: m.repo, group: m.group, gc: true });
      live.models.delete(modelKey(m));
      ok(`Removed ${m.repo}`, r.gc ? `${bytes(r.gc.freedBytes)} freed` : undefined);
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }

  async function gc() {
    collecting = true;
    try {
      const r = await api.store.gc({ partials: true });
      ok(`Freed ${bytes(r.freedBytes)}`);
    } catch (err) {
      fail(err, 'GC failed');
    } finally {
      collecting = false;
    }
  }

  async function verify(m?: StoredModel) {
    try {
      const r = await api.store.verify(m ? { sourceId: m.sourceId, repo: m.repo, group: m.group } : {});
      ok(m ? `Verifying ${m.repo}` : 'Verifying the library', undefined, r.task ? { href: `/tasks?id=${r.task.id}`, label: 'Task' } : undefined);
    } catch (err) {
      fail(err, 'Verify refused');
    }
  }

  function openExport(m: StoredModel | null) {
    exportTarget = m;
    exportOpen = true;
  }

  async function runExport() {
    exporting = true;
    try {
      const req = exportTarget ? { sourceId: exportTarget.sourceId, repo: exportTarget.repo, group: exportTarget.group, dir: exportDir } : { dir: exportDir };
      const r = await api.store.export(req);
      ok(exportTarget ? `Exporting ${exportTarget.repo}` : 'Exporting the library', exportDir, r.task ? { href: `/tasks?id=${r.task.id}`, label: 'Task' } : undefined);
      exportOpen = false;
    } catch (err) {
      fail(err, 'Export refused');
    } finally {
      exporting = false;
    }
  }

  // Quick targets beside the run dialog: one click into any slot
  function runItems(m: StoredModel) {
    return [
      ...slots.map((s) => ({ label: slotOccupied(s.id) ? `Swap into ${s.name}` : `Run in ${s.name}`, icon: slotOccupied(s.id) ? ArrowLeftRight : Play, detail: slotOccupied(s.id) ? `replaces ${s.request?.repo?.split('/').pop() ?? 'the current model'}` : 'empty', onSelect: () => launch({ sourceId: m.sourceId, repo: m.repo, group: m.group, slotId: s.id }) })),
      { label: 'Run with options', icon: SlidersHorizontal, detail: 'runtime, profile, parameters, memory plan', onSelect: () => runModel(m) },
      { label: '', separator: true },
      { label: 'Export', icon: FolderOutput, onSelect: () => openExport(m) },
      { label: 'Verify', icon: ShieldCheck, onSelect: () => verify(m) },
      { label: 'Remove', icon: Trash2, tone: 'bad' as const, onSelect: () => remove(m), disabled: servingAs(m).length > 0 }
    ];
  }
</script>

{#snippet head()}
  <thead>
    <tr>
      <SortTh id="model" label="Model" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
      {#if several}<SortTh id="source" label="Source" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />{/if}
      <SortTh id="format" label="Format" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
      <SortTh id="params" label="Params" num active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
      <SortTh id="size" label="Size" num active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
      <SortTh id="pulled" label="Pulled" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
      <SortTh id="used" label="Last run" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
      <th></th>
    </tr>
  </thead>
{/snippet}

<PageHeader title="Models">
  {#snippet meta()}
    <ModelsNav />
  {/snippet}
  <Menu
    label="Library"
    icon={HardDrive}
    items={[
      { label: 'Verify everything', icon: ShieldCheck, detail: 'rehash every blob, delete corrupt ones', onSelect: () => verify() },
      { label: 'Collect garbage', icon: Recycle, detail: 'delete blobs and partial pulls nothing references', onSelect: gc },
      { label: 'Export as a mirror', icon: FolderOutput, detail: 'a directory another nebu can pull from', onSelect: () => openExport(null), disabled: live.models.size === 0 }
    ]}
  />
  <Button variant="primary" icon={Compass} href="/catalog">Discover models</Button>
</PageHeader>

<div class="flex flex-col gap-5">
  {#if status}
    <div class="card flex flex-col gap-3 px-5 py-4 sm:flex-row sm:items-center sm:gap-6">
      <div class="flex items-baseline gap-2">
        <span class="text-2xl font-semibold tabular-nums text-fg">{bytes(onDisk)}</span>
        <span class="text-sm text-fg-muted">on disk{ceiling ? ` of ${bytes(ceiling, 0)}` : ''}</span>
      </div>
      <div class="min-w-0 flex-1">
        {#if ceiling}<Meter value={onDisk} max={ceiling} />{/if}
        <div class="mt-1.5 truncate text-xs text-fg-faint" title={diskLine}>{diskLine || status.path}</div>
      </div>
      <div class="text-sm text-fg-muted"><span class="tabular-nums text-fg">{count(live.models.size)}</span> {live.models.size === 1 ? 'model' : 'models'} · <span class="tabular-nums text-fg">{count(status.blobs)}</span> blobs</div>
    </div>
  {/if}

  <Card flush>
    {#snippet actions()}
      {#if several}
        <select class="input h-8 w-auto" bind:value={sourceFilter} aria-label="Source">
          <option value="">All sources</option>
          {#each sourceIds as id (id)}<option value={id}>{sourceName(id)}</option>{/each}
        </select>
      {/if}
      <div class="relative">
        <Search size={14} class="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-fg-faint" />
        <input bind:this={filterInput} class="input h-8 w-64 pr-8 pl-9" placeholder="Filter the library" bind:value={filter} />
        <span class="kbd pointer-events-none absolute top-1/2 right-2 -translate-y-1/2 {filter ? 'hidden' : ''}">/</span>
      </div>
    {/snippet}
    {#if loading}
      <table class="tbl">
        {@render head()}
        <tbody><SkeletonRows rows={3} cols={[{ w: 'w-56', sub: true }, 'w-12', { w: 'w-10', num: true }, { w: 'w-14', num: true }, 'w-14', 'w-14', { w: 'w-20', num: true }]} /></tbody>
      </table>
    {:else if live.models.size === 0}
      <Empty icon={Boxes} title="Nothing in the library">
        <Button variant="primary" icon={Compass} href="/catalog">Discover models</Button>
      </Empty>
    {:else if models.length === 0}
      <Empty compact title="Nothing matches" />
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          {@render head()}
          <tbody>
            {#each models as m (modelKey(m))}
              {@const key = modelKey(m)}
              {@const serving = servingAs(m)}
              {@const on = drawerOpen && selected && modelKey(selected) === key}
              <tr class="row-link {on ? 'row-active' : ''}" onclick={() => openModel(m)}>
                <td>
                  <div class="flex items-center gap-2">
                    <span class="font-medium text-fg">{m.repo}</span>
                    {#each serving as name (name)}<Pill tone="ok" dot label="serving as {name}" />{/each}
                  </div>
                  <div class="font-mono text-xs text-fg-muted">{m.group}{#if m.descriptor?.architecture}<span class="font-sans">{' · '}{m.descriptor.architecture}</span>{/if}</div>
                </td>
                {#if several}<td class="text-fg-muted">{sourceName(m.sourceId)}</td>{/if}
                <td class="text-fg-muted">{m.formatId}</td>
                <td class="num">{fmtParams(m.descriptor?.parameterCount)}</td>
                <td class="num">{bytes(m.bytes)}</td>
                <td class="text-fg-muted" title={when(m.pulledAt)}>{ago(m.pulledAt, clock.now)}</td>
                <td class="text-fg-muted" title={when(usedAt(m))}>{m.usedAt ? ago(m.usedAt, clock.now) : 'never'}</td>
                <td class="actions" onclick={(e) => e.stopPropagation()}>
                  <span class="inline-flex items-center gap-1.5">
                    <Button size="sm" variant="primary" icon={Play} onclick={() => runModel(m)}>Run</Button>
                    <Menu size="sm" items={runItems(m)} />
                  </span>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Card>
</div>

<Dialog bind:open={exportOpen} title={exportTarget ? `Export ${exportTarget.repo}` : 'Export the library'} description="Writes a mirror another nebu can pull from">
  <Field label="Directory" for="export-dir" hint="On the daemon's host">
    <input id="export-dir" class="input font-mono" bind:value={exportDir} placeholder="/mnt/mirror" autocomplete="off" spellcheck="false" />
  </Field>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (exportOpen = false)}>Cancel</Button>
    <Button variant="primary" icon={HardDrive} loading={exporting} disabled={!exportDir.trim()} onclick={runExport}>Export</Button>
  {/snippet}
</Dialog>

{#if selected}
  <ModelDrawer bind:open={drawerOpen} sourceId={selected.sourceId} sourceLabel={sourceName(selected.sourceId)} repo={selected.repo} revision={selected.revision} caps={capsOf(selected.sourceId)} runtimes={cached.runtimes} bind:slotId />
{/if}
