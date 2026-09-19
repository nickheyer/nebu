<script lang="ts">
  import type { Part } from '$proto/estimate_pb';
  import { storage } from '$lib/format';
  import State from './ui/State.svelte';

  let { parts, compact = false }: { parts: Part[]; compact?: boolean } = $props();

  function tone(p: Part): 'ok' | 'info' | 'bad' | 'warn' {
    if (p.error) return p.required ? 'bad' : 'warn';
    if (p.stored || p.bundled) return 'ok';
    return 'info';
  }
  function word(p: Part): string {
    if (p.error) return p.required ? 'missing' : 'not found';
    if (p.stored) return 'stored';
    if (p.bundled) return 'included';
    return 'download';
  }
  // Show a bundled subfolder or the download location.
  function from(p: Part): string {
    if (p.error) return p.error;
    if (p.bundled) return p.subfolder ? (p.sizeBytes > 0n ? `${p.subfolder} · ${storage(p.sizeBytes)}` : p.subfolder) : '';
    const where = p.paths.length ? `${p.repo} · ${p.paths.join(', ')}` : `${p.repo} · ${p.group}`;
    return p.sizeBytes > 0n ? `${where} · ${storage(p.sizeBytes)}` : where;
  }
</script>

<ul class="flex flex-col divide-y divide-line/50 {compact ? 'text-xs' : 'text-sm'}">
  {#each parts as p (p.slot + p.param)}
    <li class="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 py-1.5">
      <span class="min-w-0 text-fg">{p.label}{#if !p.required}<span class="text-fg-faint"> · optional</span>{/if}</span>
      <span class="min-w-0 text-fg-muted">{p.name}</span>
      <span class="ml-auto shrink-0"><State tone={tone(p)} label={word(p)} /></span>
      {#if from(p)}<span class="basis-full font-mono text-xs text-fg-faint wrap-anywhere">{from(p)}</span>{/if}
    </li>
  {/each}
</ul>
