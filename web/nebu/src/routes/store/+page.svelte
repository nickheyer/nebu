<script lang="ts">
  import { api } from '$lib/api';
  import { live, clock, modelKey, instanceLive } from '$lib/state.svelte';
  import { runModel } from '$lib/slotActions.svelte';
  import { dragModel } from '$lib/dnd.svelte';
  import { ago, bytes, count, params as fmtParams, when } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import type { StoredModel } from '$proto/store_pb';
  import { Database, GripVertical, Play, FolderOutput, ShieldCheck, Trash2, Search, Recycle, HardDrive } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Panel from '$lib/components/ui/Panel.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import Stat from '$lib/components/ui/Stat.svelte';
  import Dialog from '$lib/components/ui/Dialog.svelte';
  import Field from '$lib/components/ui/Field.svelte';
  import SlotRail from '$lib/components/SlotRail.svelte';
  import SortTh from '$lib/components/ui/SortTh.svelte';
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
  const sort = new TableSort('pulled');

  const sourceIds = $derived([...new Set([...live.models.values()].map((m) => m.sourceId))].sort());
  // Eviction takes the model used longest ago, a model never run counting from its pull
  const usedAt = (m: StoredModel) => m.usedAt ?? m.pulledAt;
  const models = $derived(
    sort.apply(
      [...live.models.values()].filter((m) => (!sourceFilter || m.sourceId === sourceFilter) && (!filter || `${m.repo} ${m.group} ${m.formatId} ${m.descriptor?.architecture ?? ''}`.toLowerCase().includes(filter.toLowerCase()))),
      (m, key) => {
        switch (key) {
          case 'model':
            return `${m.repo} ${m.group}`;
          case 'format':
            return m.formatId;
          case 'arch':
            return m.descriptor?.architecture ?? '';
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
  const total = $derived([...live.models.values()].reduce((a, m) => a + m.bytes, 0n));

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
      ok('Collected', `${bytes(r.freedBytes)} freed`);
    } catch (err) {
      fail(err, 'Collect failed');
    } finally {
      collecting = false;
    }
  }

  async function verify(m?: StoredModel) {
    try {
      const r = await api.store.verify(m ? { sourceId: m.sourceId, repo: m.repo, group: m.group } : {});
      ok(m ? `Verifying ${m.repo}` : 'Verifying the store', undefined, r.task ? { href: `/tasks?id=${r.task.id}`, label: 'Task' } : undefined);
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
      ok(exportTarget ? `Exporting ${exportTarget.repo}` : 'Exporting the store', exportDir, r.task ? { href: `/tasks?id=${r.task.id}`, label: 'Task' } : undefined);
      exportOpen = false;
    } catch (err) {
      fail(err, 'Export refused');
    } finally {
      exporting = false;
    }
  }
</script>

<PageHeader title="Store">
  {#snippet meta()}
    {#if status}<span class="font-mono">{status.path}</span>{/if}
  {/snippet}
  <Button variant="outline" icon={ShieldCheck} onclick={() => verify()}>Verify</Button>
  <Button variant="outline" icon={Recycle} loading={collecting} onclick={gc}>Collect</Button>
  <Button variant="outline" icon={FolderOutput} onclick={() => openExport(null)} disabled={live.models.size === 0}>Export</Button>
</PageHeader>

<div class="mb-5 grid grid-cols-2 divide-x divide-line rounded-lg border border-line md:grid-cols-4">
  <div class="px-4 py-3"><Stat label="Models" value={count(live.models.size)} /></div>
  <div class="px-4 py-3"><Stat label="On disk" value={bytes(status?.blobBytes ?? total)} sub={status?.maxBytes ? `${count(status?.blobs)} blobs · cap ${bytes(status.maxBytes, 0)}` : `${count(status?.blobs)} blobs`} /></div>
  <div class="px-4 py-3"><Stat label="Referenced" value={bytes(total)} /></div>
  <div class="px-4 py-3"><Stat label="Partial pulls" value={count(status?.partials ?? 0)} sub={status?.partials ? bytes(status.partialBytes) : ''} /></div>
</div>

<div class="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1fr)_17rem]">
  <Panel flush>
    {#snippet actions()}
      {#if sourceIds.length > 1}
        <select class="input h-8 w-auto py-0 pr-7 text-xs" bind:value={sourceFilter} aria-label="Source">
          <option value="">All sources</option>
          {#each sourceIds as id (id)}<option value={id}>{id}</option>{/each}
        </select>
      {/if}
      <div class="relative">
        <Search size={13} class="pointer-events-none absolute top-1/2 left-2.5 -translate-y-1/2 text-fg-faint" />
        <input class="input h-8 w-64 pl-8" placeholder="Filter" bind:value={filter} />
      </div>
    {/snippet}
    {#if live.models.size === 0}
      <Empty icon={Database} title="Nothing stored">
        <Button size="sm" variant="primary" href="/catalog">Catalog</Button>
      </Empty>
    {:else if models.length === 0}
      <Empty compact title="No matches" />
    {:else}
      <div class="overflow-x-auto">
        <table class="tbl">
          <thead>
            <tr>
              <th class="w-6"></th>
              <SortTh id="model" label="model" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
              <SortTh id="format" label="format" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
              <SortTh id="arch" label="arch" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
              <SortTh id="params" label="params" num active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
              <SortTh id="size" label="size" num active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
              <SortTh id="pulled" label="pulled" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
              <SortTh id="used" label="used" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
              <th></th>
            </tr>
          </thead>
          <tbody>
            {#each models as m (modelKey(m))}
              {@const key = modelKey(m)}
              {@const running = [...live.instances.values()].filter((i) => i.sourceId === m.sourceId && i.repo === m.repo && i.group === m.group && instanceLive(i))}
              <tr draggable="true" ondragstart={(e) => dragModel(e, key)} class="cursor-grab active:cursor-grabbing">
                <td class="text-fg-faint"><GripVertical size={14} /></td>
                <td>
                  <div class="font-mono text-sm text-fg">{m.repo}{#if sourceIds.length > 1}<span class="ml-1.5 rounded bg-raised px-1 text-[10.5px] text-fg-faint">{m.sourceId}</span>{/if}</div>
                  <div class="flex items-center gap-2 font-mono text-xs text-fg-muted">
                    {m.group}
                    {#if running.length}<span class="text-[11px] text-ok">serving as {running.map((i) => i.name).join(', ')}</span>{/if}
                  </div>
                </td>
                <td class="text-xs">{m.formatId}</td>
                <td class="text-xs">{m.descriptor?.architecture || '–'}</td>
                <td class="num text-xs">{fmtParams(m.descriptor?.parameterCount)}</td>
                <td class="num text-xs">{bytes(m.bytes)}</td>
                <td class="text-xs text-fg-muted" title={when(m.pulledAt)}>{ago(m.pulledAt, clock.now)}</td>
                <td class="text-xs text-fg-muted" title={when(usedAt(m))}>{ago(usedAt(m), clock.now)}</td>
                <td class="text-right whitespace-nowrap">
                  <span class="inline-flex items-center gap-1">
                    <Button size="xs" variant="primary" icon={Play} onclick={() => runModel(m)}>Run</Button>
                    <Menu
                      items={[
                        { label: 'Run', icon: Play, onSelect: () => runModel(m) },
                        { label: 'Export', icon: FolderOutput, onSelect: () => openExport(m) },
                        { label: 'Verify', icon: ShieldCheck, onSelect: () => verify(m) },
                        { label: '', separator: true },
                        { label: 'Remove', icon: Trash2, tone: 'bad', onSelect: () => remove(m), disabled: running.length > 0 }
                      ]}
                    />
                  </span>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Panel>
  <SlotRail />
</div>

<Dialog bind:open={exportOpen} title={exportTarget ? `Export ${exportTarget.repo}` : 'Export the store'} description="A mirror layout another nebu can pull from">
  <Field label="Directory on the daemon host" for="export-dir">
    <input id="export-dir" class="input font-mono" bind:value={exportDir} placeholder="/mnt/mirror" />
  </Field>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (exportOpen = false)}>Cancel</Button>
    <Button variant="primary" icon={HardDrive} loading={exporting} disabled={!exportDir.trim()} onclick={runExport}>Export</Button>
  {/snippet}
</Dialog>
