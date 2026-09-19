<script lang="ts">
  import { ArtifactRole, type Artifact } from '$proto/model_pb';
  import { enumLabel, storage, plural } from '$lib/format';
  import { weightsName } from '$lib/catalog';
  import { fileUrl } from '$lib/api';
  import { copyText, downloadUrl } from '$lib/clipboard';
  import { ok, fail } from '$lib/toast.svelte';
  import { TableSort } from '$lib/sort.svelte';
  import { Folder, File, FileCode, FileText, Check, ChevronRight, CornerLeftUp, Download, ExternalLink, Link } from '@lucide/svelte';
  import type { Component } from 'svelte';
  import Empty from './ui/Empty.svelte';
  import Tip from './ui/Tip.svelte';
  import SortTh from './ui/SortTh.svelte';
  import Menu from './ui/Menu.svelte';

  let {
    files,
    stored = new Set<string>(),
    sourceId,
    sourceName = 'the source',
    repo,
    revision = ''
  }: { files: Artifact[]; stored?: Set<string>; sourceId: string; sourceName?: string; repo: string; revision?: string } = $props();

  let dir = $state('');
  const sort = new TableSort('name', 'asc');

  interface Row {
    name: string;
    path: string;
    folder: boolean;
    count: number;
    bytes: bigint;
    file?: Artifact;
  }

  const rows = $derived.by(() => {
    const prefix = dir ? dir + '/' : '';
    const folders = new Map<string, Row>();
    const out: Row[] = [];
    for (const a of files) {
      if (!a.path.startsWith(prefix)) continue;
      const rest = a.path.slice(prefix.length);
      const slash = rest.indexOf('/');
      if (slash < 0) {
        out.push({ name: rest, path: a.path, folder: false, count: 1, bytes: a.sizeBytes, file: a });
        continue;
      }
      const name = rest.slice(0, slash);
      let f = folders.get(name);
      if (!f) {
        f = { name, path: prefix + name, folder: true, count: 0, bytes: 0n };
        folders.set(name, f);
        out.push(f);
      }
      f.count++;
      f.bytes += a.sizeBytes;
    }
    const ordered = sort.apply(out, (r, key) => {
      switch (key) {
        case 'role':
          return r.folder ? '' : enumLabel(ArtifactRole, r.file?.role);
        case 'weights':
          return r.file ? belongs(r.file) : '';
        case 'size':
          return r.bytes;
        default:
          return r.name.toLowerCase();
      }
    });
    return sort.key === 'name' ? ordered.sort((a, b) => Number(b.folder) - Number(a.folder)) : ordered;
  });
  const total = $derived(files.reduce((a, f) => a + f.sizeBytes, 0n));
  const here = $derived(rows.reduce((a, r) => a + r.bytes, 0n));
  const crumbs = $derived(dir ? dir.split('/') : []);
  const parent = $derived(dir.includes('/') ? dir.slice(0, dir.lastIndexOf('/')) : '');
  const inStore = $derived(files.filter((f) => stored.has(f.path)).length);

  const icons: Partial<Record<ArtifactRole, Component<any>>> = {
    [ArtifactRole.CODE]: FileCode,
    [ArtifactRole.CONFIG]: FileText,
    [ArtifactRole.TOKENIZER]: FileText,
    [ArtifactRole.TEMPLATE]: FileText,
    [ArtifactRole.INDEX]: FileText
  };
  function belongs(a: Artifact): string {
    return a.role === ArtifactRole.WEIGHTS || a.role === ArtifactRole.PROJECTOR || a.role === ArtifactRole.DRAFT ? weightsName(a.group, a.formatId) : '';
  }
  function shard(a: Artifact): string {
    return a.shardCount > 1 ? `shard ${a.shardIndex} of ${a.shardCount}` : '';
  }
  async function copyPath(path: string) {
    if (await copyText(path)) ok('Copied ' + path);
    else fail('The clipboard refused', 'Copy failed');
  }
  function items(a: Artifact) {
    const name = a.path.split('/').pop() ?? a.path;
    const list: { label: string; icon: Component<any>; detail?: string; onSelect: () => void }[] = [
      { label: 'Download file', icon: Download, detail: storage(a.sizeBytes), onSelect: () => downloadUrl(fileUrl(sourceId, repo, revision, a.path), name) },
      { label: 'Copy path', icon: Link, onSelect: () => copyPath(a.path) }
    ];
    if (a.url) {
      list.push({
        label: `Open on ${sourceName}`,
        icon: ExternalLink,
        onSelect: () => {
          window.open(a.url, '_blank', 'noopener');
        }
      });
    }
    return list;
  }
</script>

{#if !files.length}
  <Empty compact title="No files listed" />
{:else}
  <div class="flex flex-col gap-3">
    <div class="flex flex-wrap items-center gap-x-1 gap-y-1 text-sm">
      <button type="button" class="inline-flex items-center gap-1.5 rounded-sm px-1 font-mono text-fg-muted transition-colors hover:text-fg {dir ? '' : 'text-fg'}" onclick={() => (dir = '')}>
        <Folder size={13} />
        <span>/</span>
      </button>
      {#each crumbs as c, i (i)}
        {@const last = i === crumbs.length - 1}
        <ChevronRight size={12} class="text-fg-faint" />
        <button type="button" class="rounded-sm px-1 font-mono transition-colors {last ? 'text-fg' : 'text-fg-muted hover:text-fg'}" onclick={() => (dir = crumbs.slice(0, i + 1).join('/'))}>{c}</button>
      {/each}
      <span class="ml-auto text-xs tabular-nums text-fg-faint">
        {#if dir}{plural(rows.reduce((a, r) => a + r.count, 0), 'file')} · {storage(here)} here · {/if}{plural(files.length, 'file')} · {storage(total)}{#if inStore}<span> · {inStore} downloaded</span>{/if}
      </span>
    </div>
    <table class="tbl">
      <thead>
        <tr>
          <SortTh id="name" label="Name" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
          <SortTh id="role" label="Role" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
          <SortTh id="weights" label="Weights" active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
          <SortTh id="size" label="Size" num active={sort.key} dir={sort.dir} onSort={(k) => sort.toggle(k)} />
          <th class="w-8"></th>
          <th class="w-8"></th>
        </tr>
      </thead>
      <tbody>
        {#if dir}
          <tr class="row-link" onclick={() => (dir = parent)}>
            <td colspan="6">
              <span class="inline-flex items-center gap-2 font-mono text-xs text-fg-muted"><CornerLeftUp size={13} class="text-fg-faint" />..</span>
            </td>
          </tr>
        {/if}
        {#each rows as r (r.path)}
          {#if r.folder}
            <tr class="row-link" onclick={() => (dir = r.path)}>
              <td class="w-full max-w-0" title={r.path}>
                <div class="flex min-w-0 items-center gap-2 font-mono text-xs text-fg"><Folder size={13} class="shrink-0 text-fg-faint" /><span class="truncate">{r.name}</span></div>
              </td>
              <td class="whitespace-nowrap text-fg-faint">{plural(r.count, 'file')}</td>
              <td></td>
              <td class="num text-fg-muted">{storage(r.bytes)}</td>
              <td></td>
              <td></td>
            </tr>
          {:else if r.file}
            {@const a = r.file}
            {@const Icon = icons[a.role] ?? File}
            {@const variant = belongs(a)}
            {@const part = shard(a)}
            <tr class="group">
              <td class="w-full max-w-0" title={a.path}>
                <div class="flex min-w-0 items-center gap-2 font-mono text-xs"><Icon size={13} class="shrink-0 text-fg-faint" /><span class="truncate text-fg">{r.name}</span></div>
              </td>
              <td class="whitespace-nowrap text-fg-muted">{enumLabel(ArtifactRole, a.role)}</td>
              <td class="whitespace-nowrap">
                {#if variant}<span class="rounded-sm bg-raised px-1.5 py-0.5 font-mono text-[11px] text-fg-muted">{variant}</span>{/if}
                {#if part}<span class="ml-2 text-xs text-fg-faint">{part}</span>{/if}
              </td>
              <td class="num">{storage(a.sizeBytes)}</td>
              <td class="w-8 text-right">
                {#if stored.has(a.path)}<Tip text="Downloaded"><Check size={13} class="text-ok" /></Tip>{/if}
              </td>
              <td class="w-8 !py-0 text-right">
                <span class="inline-flex invisible group-hover:visible has-[[data-state=open]]:visible"><Menu size="sm" items={items(a)} /></span>
              </td>
            </tr>
          {/if}
        {/each}
      </tbody>
    </table>
  </div>
{/if}
