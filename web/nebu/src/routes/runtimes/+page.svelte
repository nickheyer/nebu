<script lang="ts">
  import { live, cached, refreshCached, taskFor, installsOf } from '$lib/state.svelte';
  import { enumLabel, middle } from '$lib/format';
  import { selectionParam } from '$lib/selection.svelte';
  import { InstallKind } from '$proto/runtime_pb';
  import { Download, RefreshCw, Cpu } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import State from '$lib/components/ui/State.svelte';
  import TaskChip from '$lib/components/TaskChip.svelte';
  import RuntimeDrawer from '$lib/components/RuntimeDrawer.svelte';
  import TaskDrawer from '$lib/components/TaskDrawer.svelte';

  const sel = selectionParam('/runtimes');
  let tab = $state('overview');
  let taskId = $state('');

  const runtimes = $derived(cached.runtimes);

  function open(id: string, t = 'overview') {
    tab = t;
    sel.id = id;
  }
</script>

<PageHeader title="Runtimes" />

{#if !cached.loaded}
  <div class="flex flex-col gap-1.5" aria-busy="true">
    {#each [0, 1, 2, 3] as i (i)}<div class="skeleton h-16"></div>{/each}
  </div>
{:else if cached.error && runtimes.length === 0}
  <Empty title={cached.error}>
    <Button size="sm" icon={RefreshCw} onclick={() => refreshCached()}>Retry</Button>
  </Empty>
{:else if runtimes.length === 0}
  <Empty icon={Cpu} title="No runtime manifests" />
{:else}
  <div class="flex flex-col gap-1.5">
    {#each runtimes as rt (rt.manifest?.id)}
      {@const id = rt.manifest?.id ?? ''}
      {@const have = installsOf(id)}
      {@const newest = have[0]}
      {@const task = taskFor('install', { runtime: id }) ?? taskFor('build', { runtime: id })}
      {@const rail = task ? 'bg-accent' : have.length ? 'bg-ok' : !rt.compatible ? 'bg-warn' : 'bg-line-strong'}
      <article class="bay grid grid-cols-[minmax(8rem,12rem)_minmax(0,1fr)_auto] items-center gap-x-6 py-3 pr-2 pl-4 {sel.id === id ? 'ring-1 ring-accent/40' : ''}">
        <span class="absolute inset-y-2.5 left-0 w-0.5 rounded-full {rail}"></span>
        <button type="button" class="min-w-0 text-left" onclick={() => open(id)}>
          <div class="truncate text-sm font-semibold text-fg">{rt.manifest?.name || id}</div>
          <div class="mt-1">
            {#if task}
              <State tone="accent" pulse label={task.kind === 'build' ? 'Building' : 'Installing'} />
            {:else if have.length}
              <State tone="ok" label="Installed" />
            {:else if !rt.compatible}
              <State tone="warn" label="Incompatible" />
            {:else}
              <State tone="neutral" label="Not installed" />
            {/if}
          </div>
        </button>
        <button type="button" class="min-w-0 text-left" onclick={() => open(id)}>
          {#if task}
            <TaskChip {task} />
          {:else if have.length}
            <div class="truncate text-sm text-fg">{newest?.version || 'unknown version'} <span class="text-fg-muted">· {enumLabel(InstallKind, newest?.kind)}{have.length > 1 ? ` · ${have.length} installs` : ''}</span></div>
            <div class="mt-1 truncate font-mono text-xs text-fg-faint" title={newest?.path}>{middle(newest?.path ?? '', 64)}</div>
          {:else if !rt.compatible}
            <div class="truncate text-sm text-fg-muted" title={rt.unmet.join('\n')}>Needs {rt.unmet.join(', ')}</div>
            <div class="mt-1 truncate text-xs text-fg-faint" title={rt.manifest?.description}>{rt.manifest?.description}</div>
          {:else}
            <div class="truncate text-sm text-fg-muted" title={rt.manifest?.description}>{rt.manifest?.description}</div>
            <div class="mt-1 truncate font-mono text-xs text-fg-faint">{(rt.manifest?.formats ?? []).join(' ')}</div>
          {/if}
        </button>
        <div class="flex items-center gap-1">
          {#if rt.compatible && rt.installs.length && !task}
            <Button size="sm" variant={have.length ? 'subtle' : 'primary'} icon={Download} onclick={() => open(id, 'install')}>Install</Button>
          {/if}
        </div>
      </article>
    {/each}
  </div>
{/if}

<RuntimeDrawer bind:id={sel.id} bind:tab onTask={(id) => (taskId = id)} />
<TaskDrawer bind:id={taskId} />
