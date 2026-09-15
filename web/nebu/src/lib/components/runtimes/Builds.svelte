<script lang="ts">
  import { SvelteSet } from 'svelte/reactivity';
  import { ChevronRight, Hammer, ScrollText, Trash2 } from '@lucide/svelte';
  import { clock, homePath } from '$lib/state.svelte';
  import { ago, duration, when } from '$lib/format';
  import { factItems, removeBuild, sandboxName } from '$lib/runtimes';
  import { BuildState, type Build } from '$proto/recipe_pb';
  import State from '$lib/components/ui/State.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Kv from '$lib/components/ui/Kv.svelte';
  import ParamList from '$lib/components/ui/ParamList.svelte';

  let { builds }: { builds: Build[] } = $props();
  const open = new SvelteSet<string>();
</script>

{#if !builds.length}
  <Empty compact icon={Hammer} title="No builds" />
{:else}
  <div class="overflow-x-auto">
    <table class="tbl">
      <thead><tr><th>Variant</th><th>Ref</th><th>State</th><th>Sandbox</th><th class="num">Took</th><th class="num">Started</th><th></th></tr></thead>
      <tbody>
        {#each builds as b (b.id)}
          {@const opened = open.has(b.id)}
          <tr class="row-link {opened ? 'row-active' : ''}" onclick={() => (opened ? open.delete(b.id) : open.add(b.id))}>
            <td>
              <div class="flex items-center gap-2">
                <ChevronRight size={12} class="shrink-0 text-fg-faint transition-transform {opened ? 'rotate-90' : ''}" />
                <span class="font-medium text-fg">{b.variant || b.recipeId}</span>
              </div>
            </td>
            <td class="font-mono text-xs text-fg-muted">{b.ref || '–'}{#if b.commit}<span class="ml-1.5 text-fg-faint">{b.commit.slice(0, 7)}</span>{/if}</td>
            <td><State values={BuildState} value={b.state} /></td>
            <td class="text-fg-muted">{sandboxName[b.sandbox]}{#if b.image}<span class="ml-1.5 font-mono text-xs text-fg-faint">{b.image}</span>{/if}</td>
            <td class="num text-fg-muted">{duration(b.createdAt, b.finishedAt, clock.now)}</td>
            <td class="num text-fg-muted" title={when(b.createdAt)}>{ago(b.createdAt, clock.now)}</td>
            <td class="actions" onclick={(e) => e.stopPropagation()}>
              <span>
                {#if b.taskId}<IconButton size="sm" icon={ScrollText} label="Log" href="/tasks/{b.taskId}" />{/if}
                <IconButton size="sm" icon={Trash2} label="Remove" onclick={() => removeBuild(b)} />
              </span>
            </td>
          </tr>
          {#if opened}
            <tr>
              <td colspan="7" class="bg-sunken/40 !py-3 !pr-4 !pl-8">
                <div class="flex max-w-3xl flex-col gap-3">
                  {#if b.error}<div class="note note-bad">{b.error}</div>{/if}
                  <Kv mono omitEmpty items={[['Binary', homePath(b.binary)], ['Directory', homePath(b.dir)], ['Install', b.installId], ['Patches', b.patches.join(', ')], ['Id', b.id], ...factItems(b.facts)]} />
                  {#if Object.keys(b.vars).length}<ParamList params={b.vars} />{/if}
                </div>
              </td>
            </tr>
          {/if}
        {/each}
      </tbody>
    </table>
  </div>
{/if}
