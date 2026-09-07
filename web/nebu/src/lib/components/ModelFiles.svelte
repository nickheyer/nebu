<script lang="ts">
  import { ArtifactRole, type Artifact } from '$proto/model_pb';
  import { enumLabel, storage, plural } from '$lib/format';
  import { weightsName } from '$lib/catalog';
  import { Folder, File, FileCode, FileText, Check, ChevronRight, CornerLeftUp } from '@lucide/svelte';
  import type { Component } from 'svelte';
  import Empty from './ui/Empty.svelte';
  import Tip from './ui/Tip.svelte';

  // Every file of a repository as the source lists it, walked a directory at a time, each saying what it is
  // for and which weights it belongs to, with a mark on the ones already in the store
  let { files, stored = new Set<string>() }: { files: Artifact[]; stored?: Set<string> } = $props();

  let dir = $state('');

  interface Row {
    name: string;
    path: string;
    folder: boolean;
    count: number;
    bytes: bigint;
    file?: Artifact;
  }

  // What sits in the open directory: its folders first, then its files, both by name
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
    return out.sort((a, b) => Number(b.folder) - Number(a.folder) || a.name.localeCompare(b.name));
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
  // Weights and their companions say which variant they belong to, other files stand alone
  function belongs(a: Artifact): string {
    return a.role === ArtifactRole.WEIGHTS || a.role === ArtifactRole.PROJECTOR || a.role === ArtifactRole.DRAFT ? weightsName(a.group, a.formatId) : '';
  }
  function shard(a: Artifact): string {
    return a.shardCount > 1 ? `shard ${a.shardIndex} of ${a.shardCount}` : '';
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
        {#if dir}{plural(rows.reduce((a, r) => a + r.count, 0), 'file')} · {storage(here)} here · {/if}{plural(files.length, 'file')} · {storage(total)}{#if inStore}<span> · {inStore} in the store</span>{/if}
      </span>
    </div>
    <table class="tbl">
      <thead><tr><th>Name</th><th>Role</th><th>Weights</th><th class="num">Size</th><th></th></tr></thead>
      <tbody>
        {#if dir}
          <tr class="row-link" onclick={() => (dir = parent)}>
            <td colspan="5">
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
            </tr>
          {:else if r.file}
            {@const a = r.file}
            {@const Icon = icons[a.role] ?? File}
            {@const variant = belongs(a)}
            {@const part = shard(a)}
            <tr>
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
                {#if stored.has(a.path)}<Tip text="In the store"><Check size={13} class="text-ok" /></Tip>{/if}
              </td>
            </tr>
          {/if}
        {/each}
      </tbody>
    </table>
  </div>
{/if}
