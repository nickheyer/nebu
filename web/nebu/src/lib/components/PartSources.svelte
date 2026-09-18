<script lang="ts">
  import { api } from '$lib/api';
  import { live, startedTask, taskActive } from '$lib/state.svelte';
  import { fail } from '$lib/toast.svelte';
  import { storage } from '$lib/format';
  import { weightsName } from '$lib/catalog';
  import { ArtifactRole, type Model } from '$proto/model_pb';
  import type { MissingPart, PartSource } from '$proto/estimate_pb';
  import { Download } from '@lucide/svelte';
  import Button from './ui/Button.svelte';
  import Menu, { type MenuItem } from './ui/Menu.svelte';
  import TaskChip from './TaskChip.svelte';

  // Where a part the store lacks is published: one file downloads at a click, a repository holding several
  // lists its weights to download one; a download under way shows its progress in place
  let { part }: { part: MissingPart } = $props();

  let resolved = $state<Record<string, Model | null>>({});
  let resolveError = $state<Record<string, string>>({});

  const key = (src: PartSource) => `${src.sourceId}\n${src.repo}`;

  async function pull(src: PartSource, group: string) {
    try {
      const r = await api.store.pull({ sourceId: src.sourceId, repo: src.repo, group });
      startedTask(`Downloading ${part.label}`, `Downloaded ${part.label}`, `${src.repo} ${src.path || group}`, r.task);
    } catch (err) {
      fail(err, 'Download refused');
    }
  }

  // The pull of this repository under way, the named group's when the source names one
  function pulling(src: PartSource) {
    return [...live.tasks.values()].find((t) => t.kind === 'pull' && taskActive(t) && t.labels.source === src.sourceId && t.labels.repo === src.repo && (!src.group || t.labels.group === src.group));
  }

  async function resolve(src: PartSource) {
    const k = key(src);
    if (k in resolved) return;
    resolved = { ...resolved, [k]: null };
    try {
      const r = await api.sources.resolve({ sourceId: src.sourceId, repo: src.repo });
      resolved = { ...resolved, [k]: r.model ?? null };
      if (!r.model) resolveError = { ...resolveError, [k]: 'No files' };
    } catch (err) {
      resolveError = { ...resolveError, [k]: err instanceof Error ? err.message : String(err) };
    }
  }

  // The weights the repository holds, one download per group, sized by every file the group spans
  function groupItems(src: PartSource): MenuItem[] {
    const k = key(src);
    const m = resolved[k];
    if (resolveError[k]) return [{ label: resolveError[k], disabled: true }];
    if (!m) return [{ label: 'Loading…', disabled: true }];
    const groups = new Map<string, { formatId: string; bytes: bigint }>();
    for (const a of m.artifacts) {
      if (a.role !== ArtifactRole.WEIGHTS || !a.group) continue;
      const g = groups.get(a.group) ?? { formatId: a.formatId, bytes: 0n };
      g.bytes += a.sizeBytes;
      groups.set(a.group, g);
    }
    if (!groups.size) return [{ label: 'No weights', disabled: true }];
    return [...groups]
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([group, g]) => ({ label: weightsName(group, g.formatId), detail: storage(g.bytes), onSelect: () => void pull(src, group) }));
  }
</script>

<div class="flex flex-col gap-1.5">
  {#each part.sources as src (src.repo + src.path)}
    {@const task = pulling(src)}
    <div class="flex flex-wrap items-center gap-2 text-xs text-fg-muted">
      <span class="min-w-0 truncate font-mono">{src.repo}{#if src.path}<span class="text-fg-faint">{' · '}{src.path}</span>{/if}</span>
      <span class="ml-auto shrink-0">
        {#if task}
          <TaskChip {task} label="Downloading" />
        {:else if src.group}
          <Button size="sm" icon={Download} onclick={() => void pull(src, src.group)}>Download</Button>
        {:else}
          <Menu label="Download" icon={Download} size="sm" items={groupItems(src)} onOpenChange={(open) => open && void resolve(src)} />
        {/if}
      </span>
    </div>
  {/each}
</div>
