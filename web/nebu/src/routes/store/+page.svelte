<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { replaceState } from '$app/navigation';
  import { create } from '@bufbuild/protobuf';
  import { api } from '$lib/api';
  import { live, cached, clock, modelKey, instanceLive, sourceName, taskFor, storeMount, orderedSlots, startedTask, inMesh, meshNodes, holdersOf, heldElsewhere } from '$lib/state.svelte';
  import { runModel } from '$lib/actions.svelte';
  import { launch, slotOccupied } from '$lib/launch';
  import { ago, storage, params as fmtParams, plural, tail, when } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { weightsName } from '$lib/catalog';
  import { isComponent, kindLabel } from '$lib/diffusion';
  import { runtimesOf } from '$lib/runtimes';
  import Chip from '$lib/components/ui/Chip.svelte';
  import type { StoredModel } from '$proto/store_pb';
  import type { Node, StoredSummary } from '$proto/mesh_pb';
  import { DescriptorSchema, type Descriptor } from '$proto/model_pb';
  import type { Timestamp } from '@bufbuild/protobuf/wkt';
  import { Boxes, Play, FolderOutput, ShieldCheck, Trash2, Recycle, ArrowLeftRight, Compass, SlidersHorizontal, X, Network, Download } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import SizeBar from '$lib/components/ui/SizeBar.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Select from '$lib/components/ui/Select.svelte';
  import SearchInput from '$lib/components/ui/SearchInput.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import SortTh from '$lib/components/ui/SortTh.svelte';
  import SkeletonRows from '$lib/components/ui/SkeletonRows.svelte';
  import TaskChip from '$lib/components/TaskChip.svelte';
  import TextInput from '$lib/components/ui/TextInput.svelte';
  import ModelsNav from '$lib/components/ModelsNav.svelte';
  import ModelDrawer from '$lib/components/ModelDrawer.svelte';
  import { TableSort } from '$lib/sort.svelte';

  // One model the library lists: stored here, or held by other members only
  interface Row {
    key: string;
    sourceId: string;
    repo: string;
    revision: string;
    group: string;
    formatId: string;
    bytes: bigint;
    pulledAt?: Timestamp;
    usedAt?: Timestamp;
    descriptor?: Descriptor;
    // The manifest when the model is here
    model: StoredModel | null;
    // Members holding it, this node among them when it is here
    holders: Node[];
  }

  const status = $derived(live.store);
  let filter = $state('');
  let sourceFilter = $state('');
  let exportOpen = $state(false);
  let exportDir = $state('');
  let exporting = $state(false);
  let selected = $state<Row | null>(null);
  let drawerOpen = $state(false);
  let slotId = $state('');
  const sort = new TableSort('pulled');
  const loading = $derived(!live.ready && !live.error);

  function localRow(m: StoredModel): Row {
    return { key: modelKey(m), sourceId: m.sourceId, repo: m.repo, revision: m.revision, group: m.group, formatId: m.formatId, bytes: m.bytes, pulledAt: m.pulledAt, usedAt: m.usedAt, descriptor: m.descriptor, model: m, holders: holdersOf(m) };
  }
  function awayRow(key: string, s: StoredSummary, holders: Node[]): Row {
    const descriptor = create(DescriptorSchema, { formatId: s.formatId, group: s.group, architecture: s.architecture, kind: s.kind, parameterCount: s.parameterCount });
    return { key, sourceId: s.sourceId, repo: s.repo, revision: s.revision, group: s.group, formatId: s.formatId, bytes: s.bytes, pulledAt: s.pulledAt, usedAt: s.usedAt, descriptor, model: null, holders };
  }
  // Every model on the mesh: the ones here first as the store lists them, then the ones held elsewhere
  const rows = $derived([...[...live.models.values()].map(localRow), ...heldElsewhere().map((h) => awayRow(h.key, h.summary, h.holders))]);
  const sourceIds = $derived([...new Set(rows.map((r) => r.sourceId))].sort());
  const several = $derived(sourceIds.length > 1);
  const slots = $derived(orderedSlots());
  // Evict by last run, falling back to download time.
  const usedAt = (r: Row) => r.usedAt ?? r.pulledAt;
  const models = $derived(
    sort.apply(
      rows.filter((r) => (!sourceFilter || r.sourceId === sourceFilter) && (!filter || `${r.repo} ${r.group} ${r.formatId} ${r.descriptor?.architecture ?? ''} ${r.holders.map((n) => n.name).join(' ')}`.toLowerCase().includes(filter.toLowerCase()))),
      (r, key) => {
        switch (key) {
          case 'model':
            return `${r.repo} ${r.group}`;
          case 'source':
            return r.sourceId;
          case 'format':
            return r.formatId;
          case 'runs':
            return r.model
              ? runtimesOf(r.model.runtimes, cached.runtimes)
                  .map((x) => x.runtime?.id)
                  .join(',')
              : '';
          case 'nodes':
            return r.holders.map((n) => (n.self ? '' : n.name)).join(',');
          case 'params':
            return r.descriptor?.parameterCount ?? 0n;
          case 'size':
            return r.bytes;
          case 'used':
            return usedAt(r)?.seconds ?? 0n;
          default:
            return r.pulledAt?.seconds ?? 0n;
        }
      }
    )
  );

  // The difference between model and blob totals is shared storage.
  const referenced = $derived([...live.models.values()].reduce((a, m) => a + m.bytes, 0n));
  const onDisk = $derived(status?.blobBytes ?? referenced);
  const shared = $derived(referenced > onDisk ? referenced - onDisk : 0n);
  const mount = $derived(storeMount());
  const diskUsed = $derived(mount ? (mount.totalBytes > mount.freeBytes ? mount.totalBytes - mount.freeBytes : 0n) : 0n);
  const otherUse = $derived(diskUsed > onDisk ? diskUsed - onDisk : 0n);
  const facts = $derived.by(() => {
    const parts: string[] = [];
    if (status?.maxBytes) parts.push(`${storage(status.maxBytes, 0)} cap on the store`);
    if (shared) parts.push(`${storage(shared)} shared between variants`);
    if (status?.partials) parts.push(`${plural(status.partials, 'unfinished download')} using ${storage(status.partialBytes)}`);
    if (status?.caches) parts.push(`${plural(status.caches, 'tensor cache')} using ${storage(status.cacheBytes)}`);
    return parts;
  });
  const storeTask = $derived(taskFor('verify', { repo: '' }) ?? taskFor('export', { repo: '' }));

  const capsOf = (id: string) => cached.sources.find((s) => s.source?.id === id)?.capabilities;
  const mesh = $derived(inMesh());
  const nodeLabel = (n: Node) => n.name || n.id.slice(0, 8);

  // Members without the model, for pulls onto them
  function lacking(r: Row) {
    const have = new Set(r.holders.map((n) => n.id));
    return meshNodes().filter((n) => !n.self && !have.has(n.id));
  }

  // The pull under way for a model, here or onto a member
  function pulling(r: Row) {
    return taskFor('pull', { source: r.sourceId, repo: r.repo, group: r.group });
  }

  // Pulls onto a member, or here when no member is named: a pull here lands from the members
  // holding the model before its source
  async function pullTo(r: Row, node?: Node) {
    const destination = node ? ` to ${nodeLabel(node)}` : '';
    try {
      const x = await api.store.pull({ sourceId: r.sourceId, repo: r.repo, revision: r.revision, group: r.group, nodeId: node?.id ?? '' });
      startedTask(`Downloading ${tail(r.repo)}${destination}`, `Downloaded ${tail(r.repo)}${destination}`, weightsName(r.group, r.formatId), x.task);
    } catch (err) {
      fail(err, 'Download failed');
    }
  }

  function servingAs(r: Row): string[] {
    return [...live.instances.values()].filter((i) => i.sourceId === r.sourceId && i.repo === r.repo && i.group === r.group && instanceLive(i)).map((i) => (i.slotId ? (live.slots.get(i.slotId)?.name ?? i.name) : i.name));
  }

  function openModel(r: Row) {
    selected = r;
    drawerOpen = true;
    replaceState(`/store?model=${encodeURIComponent(r.key)}`, {});
  }

  // Open the model from the URL after the snapshot loads.
  let wanted = page.url.searchParams.get('model') ?? '';
  $effect(() => {
    if (!wanted || !live.ready) return;
    const r = rows.find((x) => x.key === wanted);
    wanted = '';
    if (r) untrack(() => openModel(r));
  });
  $effect(() => {
    if (!drawerOpen && !wanted && page.url.searchParams.has('model')) replaceState('/store', {});
  });

  async function remove(m: StoredModel) {
    const yes = await confirm({ title: `Remove ${tail(m.repo)} ${weightsName(m.group, m.formatId)}?`, message: 'Files not shared with another variant are deleted from disk.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      const r = await api.store.removeModel({ sourceId: m.sourceId, repo: m.repo, group: m.group, gc: true });
      live.models.delete(modelKey(m));
      ok(`Removed ${tail(m.repo)}`, r.gc ? `${storage(r.gc.freedBytes)} freed` : undefined);
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }

  async function clean() {
    try {
      const r = await api.store.gc({ partials: true });
      ok(`Freed ${storage(r.freedBytes)}`, r.removed ? plural(r.removed, 'file') + ' removed' : 'Nothing to remove');
    } catch (err) {
      fail(err, 'Clean failed');
    }
  }

  async function verify(m?: StoredModel) {
    try {
      const r = await api.store.verify(m ? { sourceId: m.sourceId, repo: m.repo, group: m.group } : {});
      startedTask(m ? `Verifying ${tail(m.repo)}` : 'Verifying every model', m ? `Verified ${tail(m.repo)}` : 'Verified every model', undefined, r.task);
    } catch (err) {
      fail(err, 'Verify refused');
    }
  }

  async function exportAll() {
    exporting = true;
    try {
      const r = await api.store.export({ dir: exportDir.trim() });
      startedTask('Exporting every model', 'Exported every model', exportDir.trim(), r.task);
      exportOpen = false;
    } catch (err) {
      fail(err, 'Export refused');
    } finally {
      exporting = false;
    }
  }

  // Pulls onto every member lacking the model, one entry each
  function pullItems(r: Row) {
    return mesh ? lacking(r).map((n) => ({ label: `Download to ${nodeLabel(n)}`, icon: Network, onSelect: () => pullTo(r, n) })) : [];
  }

  function runItems(r: Row) {
    const m = r.model;
    if (!m) {
      return [{ label: 'Download', icon: Download, detail: 'To this node', onSelect: () => pullTo(r) }, ...pullItems(r)];
    }
    if (isComponent(m.descriptor)) {
      return [
        ...pullItems(r),
        { label: 'Verify', icon: ShieldCheck, onSelect: () => verify(m) },
        { label: 'Remove', icon: Trash2, tone: 'bad' as const, onSelect: () => remove(m), disabled: servingAs(r).length > 0 }
      ];
    }
    return [
      ...slots.map((s) => ({ label: slotOccupied(s.id) ? `Swap into ${s.position}. ${s.name}` : `Run in ${s.position}. ${s.name}`, icon: slotOccupied(s.id) ? ArrowLeftRight : Play, detail: slotOccupied(s.id) ? `replaces ${tail(s.request?.repo ?? '')}` : 'empty', onSelect: () => launch({ sourceId: m.sourceId, repo: m.repo, group: m.group, slotId: s.id }) })),
      { label: 'Run with options…', icon: SlidersHorizontal, onSelect: () => runModel(m) },
      ...pullItems(r),
      { label: '', separator: true },
      { label: 'Verify', icon: ShieldCheck, onSelect: () => verify(m) },
      { label: 'Remove', icon: Trash2, tone: 'bad' as const, onSelect: () => remove(m), disabled: servingAs(r).length > 0 }
    ];
  }
</script>

{#snippet head()}
  <thead>
    <tr>
      <SortTh id="model" label="Model" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
      {#if several}<SortTh id="source" label="Source" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />{/if}
      <SortTh id="format" label="Format" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
      <SortTh id="runs" label="Runs on" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
      {#if mesh}<SortTh id="nodes" label="Stored on" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />{/if}
      <SortTh id="params" label="Params" num active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
      <SortTh id="size" label="Size" num active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
      <SortTh id="pulled" label="Downloaded" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
      <SortTh id="used" label="Last run" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
      <th></th>
    </tr>
  </thead>
{/snippet}

<PageHeader title="Models">
  {#snippet below()}
    <ModelsNav />
  {/snippet}
</PageHeader>

<div class="flex flex-col gap-10">
  <Section title="Disk" meta={status?.path ?? ''}>
    {#snippet actions()}
      {#if storeTask}<TaskChip task={storeTask} />{/if}
      <Button size="sm" icon={ShieldCheck} disabled={!live.models.size || !!storeTask} onclick={() => verify()}>Verify</Button>
      <Button size="sm" icon={Recycle} disabled={!status} onclick={clean}>Clean</Button>
      <Button size="sm" icon={FolderOutput} disabled={!live.models.size || !!storeTask} onclick={() => (exportOpen = !exportOpen)}>Export</Button>
    {/snippet}
    {#if status}
      <div class="flex flex-col gap-2">
        {#if mount}
          <SizeBar total={mount.totalBytes} units="decimal" figure="left" free="free" overlays={[{ start: 'left', items: [{ label: 'other', size: otherUse, tone: 'neutral' }, { label: 'models', size: onDisk, tone: 'accent' }] }]} />
        {:else}
          <span class="text-sm tabular-nums text-fg">{storage(onDisk)} in the store</span>
        {/if}
        <div class="flex flex-wrap gap-x-4 text-xs text-fg-faint">
          <span>{plural(live.models.size, 'model')} · {plural(status.blobs, 'blob')}</span>
          {#each facts as f (f)}<span>{f}</span>{/each}
        </div>
      </div>
    {:else}
      <div class="skeleton h-14" aria-busy="true"></div>
    {/if}
    {#if exportOpen}
      <form
        class="flex flex-wrap items-center gap-2 rounded-md border border-line bg-surface p-3"
        onsubmit={(e) => {
          e.preventDefault();
          if (exportDir.trim()) exportAll();
        }}
      >
        <span class="text-sm text-fg-muted">Export all models to a mirror directory.</span>
        <TextInput class="min-w-64 flex-1" mono bind:value={exportDir} empty="/path/to/mirror" aria-label="Export directory" />
        <Button type="submit" variant="primary" icon={FolderOutput} loading={exporting} disabled={!exportDir.trim()}>Export</Button>
        <Button variant="ghost" icon={X} aria-label="Cancel" onclick={() => (exportOpen = false)} />
      </form>
    {/if}
  </Section>

  <Section title="Library" count={rows.length || undefined}>
    {#snippet actions()}
      {#if several}
        <Select size="sm" class="w-40" bind:value={sourceFilter} label="Source" items={[{ value: '', label: 'All sources' }, ...sourceIds.map((id) => ({ value: id, label: sourceName(id) }))]} />
      {/if}
      <SearchInput class="w-64" bind:value={filter} />
    {/snippet}
    {#if loading}
      <table class="tbl">
        {@render head()}
        <tbody><SkeletonRows rows={3} cols={[{ w: 'w-56', sub: true }, 'w-12', { w: 'w-10', num: true }, { w: 'w-14', num: true }, 'w-14', 'w-14', { w: 'w-20', num: true }]} /></tbody>
      </table>
    {:else if rows.length === 0}
      <Empty icon={Boxes} title="No models downloaded yet">
        <Button variant="primary" icon={Compass} href="/catalog">Browse catalog</Button>
      </Empty>
    {:else if models.length === 0}
      <Empty compact title="No models match" />
    {:else}
      <div class="tbl-wrap">
        <table class="tbl">
          {@render head()}
          <tbody>
            {#each models as r (r.key)}
              {@const serving = servingAs(r)}
              {@const task = pulling(r)}
              {@const on = drawerOpen && selected && selected.key === r.key}
              <tr class="row-link {on ? 'row-active' : ''}" onclick={() => openModel(r)}>
                <td>
                  <div class="flex items-center gap-3">
                    <span class="text-fg">{r.repo}</span>
                    {#each serving as name (name)}<State tone="ok" label="Serving as {name}" />{/each}
                  </div>
                  <div class="flex items-center gap-2 font-mono text-xs text-fg-muted">
                    <span>{weightsName(r.group, r.formatId)}{#if r.descriptor?.architecture}<span class="font-sans">{' · '}{r.descriptor.architecture}</span>{/if}</span>
                    {#if kindLabel(r.descriptor)}<Chip text={kindLabel(r.descriptor)} mono={false} title={isComponent(r.descriptor) ? 'Requires a diffusion model' : 'Model output'} />{/if}
                  </div>
                </td>
                {#if several}<td class="text-fg-muted">{sourceName(r.sourceId)}</td>{/if}
                <td class="text-fg-muted">{r.formatId}</td>
                <td>
                  {#if r.model}
                    <div class="flex flex-wrap gap-1">
                      {#each runtimesOf(r.model.runtimes, cached.runtimes) as x (x.runtime?.id)}
                        <Chip text={x.runtime?.name ?? x.runtime?.id ?? ''} mono={false} title={x.compatible ? `${x.runtime?.name} serves this model` : `${x.runtime?.name} serves this model, but is not compatible with this host`} class={x.compatible ? '' : 'opacity-50'} />
                      {:else}
                        <span class="text-xs text-fg-faint" title="No compatible runtime">{isComponent(r.model.descriptor) ? 'part' : '–'}</span>
                      {/each}
                    </div>
                  {:else}
                    <span class="text-xs text-fg-faint" title="Download to see compatible runtimes">–</span>
                  {/if}
                </td>
                {#if mesh}
                  <td>
                    <div class="flex flex-wrap gap-1">
                      {#each r.holders as n (n.id)}<Chip text={n.self ? 'This node' : nodeLabel(n)} mono={false} />{/each}
                    </div>
                  </td>
                {/if}
                <td class="num">{fmtParams(r.descriptor?.parameterCount)}</td>
                <td class="num">{storage(r.bytes)}</td>
                <td class="text-fg-muted" title={when(r.pulledAt)}>{ago(r.pulledAt, clock.now)}</td>
                <td class="text-fg-muted" title={when(usedAt(r))}>{r.usedAt ? ago(r.usedAt, clock.now) : 'never'}</td>
                <td class="actions" onclick={(e) => e.stopPropagation()}>
                  <span>
                    {#if task}
                      <TaskChip {task} label="Downloading" />
                    {:else if !r.model}
                      <Button size="xs" variant="primary" icon={Download} title="Download to this node" onclick={() => pullTo(r)}>Download</Button>
                    {:else if !isComponent(r.model.descriptor)}
                      <Button size="xs" variant="primary" icon={Play} onclick={() => runModel(r.model)}>Run</Button>
                    {/if}
                    <Menu size="xs" items={runItems(r)} />
                  </span>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Section>
</div>

{#if selected}
  <ModelDrawer bind:open={drawerOpen} sourceId={selected.sourceId} sourceLabel={sourceName(selected.sourceId)} repo={selected.repo} revision={selected.revision} caps={capsOf(selected.sourceId)} bind:slotId />
{/if}
