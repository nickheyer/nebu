<script lang="ts">
  import { goto } from '$app/navigation';
  import { cached, refreshCached, taskFor, installsOf } from '$lib/state.svelte';
  import { enumLabel, middle } from '$lib/format';
  import { InstallKind, ApiFlavor } from '$proto/runtime_pb';
  import { Download, RefreshCw, Cpu, ChevronRight } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import State from '$lib/components/ui/State.svelte';
  import TaskChip from '$lib/components/TaskChip.svelte';

  const runtimes = $derived(cached.runtimes);
</script>

<PageHeader title="Runtimes">
  {#snippet meta()}
    <span>Inference servers nebu can install and launch. Each one is described by a manifest.</span>
  {/snippet}
</PageHeader>

{#if !cached.loaded}
  <div class="flex flex-col gap-2" aria-busy="true">
    {#each [0, 1, 2, 3] as i (i)}<div class="skeleton h-16"></div>{/each}
  </div>
{:else if cached.error && runtimes.length === 0}
  <Empty title={cached.error}>
    <Button size="sm" icon={RefreshCw} onclick={() => refreshCached()}>Retry</Button>
  </Empty>
{:else if runtimes.length === 0}
  <Empty icon={Cpu} title="No runtime manifests were loaded" />
{:else}
  <div class="flex flex-col gap-2">
    {#each runtimes as rt (rt.manifest?.id)}
      {@const id = rt.manifest?.id ?? ''}
      {@const have = installsOf(id)}
      {@const newest = have[0]}
      {@const task = taskFor('install', { runtime: id }) ?? taskFor('build', { runtime: id })}
      {@const rail = task ? 'bg-accent' : have.length ? 'bg-ok' : !rt.compatible ? 'bg-warn' : 'bg-line-strong'}
      <a href="/runtimes/{id}" class="card card-link relative grid grid-cols-[minmax(9rem,13rem)_minmax(0,1fr)_auto] items-center gap-x-6 py-3 pr-3 pl-4">
        <span class="absolute inset-y-3 left-0 w-0.5 rounded-full {rail}"></span>
        <div class="min-w-0">
          <div class="truncate text-sm font-semibold text-fg">{rt.manifest?.name || id}</div>
          <div class="mt-1">
            {#if task}
              <State tone="accent" pulse label={task.kind === 'build' ? 'Building' : 'Installing'} />
            {:else if have.length}
              <State tone="ok" label="Installed" />
            {:else if !rt.compatible}
              <State tone="warn" label="Not compatible" />
            {:else}
              <State tone="neutral" label="Not installed" />
            {/if}
          </div>
        </div>
        <div class="min-w-0">
          {#if task}
            <TaskChip {task} />
          {:else if have.length}
            <div class="truncate text-sm text-fg">{newest?.version || 'Unknown version'} <span class="text-fg-muted">· {enumLabel(InstallKind, newest?.kind)}{have.length > 1 ? ` · ${have.length} installs` : ''}</span></div>
            <div class="mt-1 truncate font-mono text-xs text-fg-faint" title={newest?.path}>{middle(newest?.path ?? '', 64)}</div>
          {:else if !rt.compatible}
            <div class="truncate text-sm text-fg-muted" title={rt.unmet.join('\n')}>Needs {rt.unmet.join(', ')}</div>
            <div class="mt-1 truncate text-xs text-fg-faint" title={rt.manifest?.description}>{rt.manifest?.description}</div>
          {:else}
            <div class="truncate text-sm text-fg-muted" title={rt.manifest?.description}>{rt.manifest?.description}</div>
            <div class="mt-1 flex flex-wrap gap-x-3 text-xs text-fg-faint"><span class="kv"><span>reads</span><span>{(rt.manifest?.formats ?? []).join(' ')}</span></span><span class="kv"><span>api</span><span>{enumLabel(ApiFlavor, rt.manifest?.launch?.api)}</span></span></div>
          {/if}
        </div>
        <div class="flex items-center gap-2">
          {#if rt.compatible && rt.installs.length && !task && !have.length}
            <Button size="sm" variant="primary" icon={Download} onclick={(e) => { e.preventDefault(); goto(`/runtimes/${id}?tab=install`); }}>Install</Button>
          {/if}
          <ChevronRight size={15} class="text-fg-faint" />
        </div>
      </a>
    {/each}
  </div>
{/if}
