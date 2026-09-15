<script lang="ts">
  import { SvelteSet } from 'svelte/reactivity';
  import { ChevronRight, Trash2 } from '@lucide/svelte';
  import { clock, homePath } from '$lib/state.svelte';
  import { ago, stateLabel, when } from '$lib/format';
  import { factItems, removeInstall } from '$lib/runtimes';
  import { InstallKind, type Install } from '$proto/runtime_pb';
  import Copy from '$lib/components/ui/Copy.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';

  let { installs }: { installs: Install[] } = $props();
  const open = new SvelteSet<string>();
</script>

<div class="overflow-x-auto">
  <table class="tbl">
    <thead><tr><th>Version</th><th>Kind</th><th>Path</th><th>Origin</th><th class="num">Added</th><th></th></tr></thead>
    <tbody>
      {#each installs as i (i.id)}
        {@const opened = open.has(i.id)}
        <tr class="row-link {opened ? 'row-active' : ''}" onclick={() => (opened ? open.delete(i.id) : open.add(i.id))}>
          <td>
            <div class="flex items-center gap-2">
              <ChevronRight size={12} class="shrink-0 text-fg-faint transition-transform {opened ? 'rotate-90' : ''}" />
              <span class="font-mono text-fg">{i.version || '–'}</span>
            </div>
          </td>
          <td class="text-fg-muted">{stateLabel(InstallKind, i.kind)}</td>
          <td class="max-w-md truncate font-mono text-xs text-fg-muted" title={i.path}>{homePath(i.path)}</td>
          <td class="max-w-xs truncate text-fg-muted" title={i.origin}>{i.origin || '–'}</td>
          <td class="num text-fg-muted" title={when(i.createdAt)}>{ago(i.createdAt, clock.now)}</td>
          <td class="actions" onclick={(e) => e.stopPropagation()}>
            <span>
              <Copy text={i.path} label="Copy path" size={13} />
              <IconButton size="sm" icon={Trash2} label="Remove" onclick={() => removeInstall(i)} />
            </span>
          </td>
        </tr>
        {#if opened}
          <tr>
            <td colspan="6" class="bg-sunken/40 !py-3 !pr-4 !pl-8">
              <Kv mono omitEmpty class="max-w-3xl" items={[['Directory', homePath(i.dir)], ['Build', i.buildId], ['Id', i.id], ...factItems(i.facts)]} />
            </td>
          </tr>
        {/if}
      {/each}
    </tbody>
  </table>
</div>
