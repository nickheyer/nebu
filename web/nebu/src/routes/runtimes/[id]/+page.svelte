<script lang="ts">
  import { page } from '$app/state';
  import { tabState } from '$lib/tabs.svelte';
  import { cached, installsOf, runtimeStatus } from '$lib/state.svelte';
  import { apiName, buildsOf, runtimeTasks } from '$lib/runtimes';
  import { InstallKind } from '$proto/runtime_pb';
  import { Package } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Tabs from '$lib/components/ui/Tabs.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Tip from '$lib/components/ui/Tip.svelte';
  import TaskChip from '$lib/components/TaskChip.svelte';
  import Installers from '$lib/components/runtimes/Installers.svelte';
  import Installs from '$lib/components/runtimes/Installs.svelte';
  import Builds from '$lib/components/runtimes/Builds.svelte';
  import Params from '$lib/components/runtimes/Params.svelte';

  const id = $derived(page.params.id ?? '');
  const status = $derived(runtimeStatus(id));
  const rt = $derived(status?.runtime);
  const installs = $derived(installsOf(id));
  const builds = $derived(buildsOf(id));
  const tasks = $derived(runtimeTasks(id));
  // The ways this host can get the runtime, laid out under its installs
  const options = $derived(status?.compatible ? status.installs.filter((o) => o.method !== undefined) : []);
  const buildable = $derived(builds.length > 0 || options.some((o) => o.method?.kind === InstallKind.BUILT));
  const tabs = $derived([
    { id: 'installs', label: 'Installs', count: installs.length },
    ...(buildable ? [{ id: 'builds', label: 'Builds', count: builds.length }] : []),
    { id: 'params', label: 'Parameters', count: rt?.params.length ?? 0 }
  ]);
  const tab = tabState(() => tabs.map((t) => t.id), () => 'installs');
</script>

{#if !cached.loaded}
  <div class="skeleton h-40" aria-busy="true"></div>
{:else if !status || !rt}
  <PageHeader title="Runtime not found" back={{ href: '/runtimes', label: 'Runtimes' }} />
  <Empty title="No runtime {id}">
    <Button href="/runtimes">Back to Runtimes</Button>
  </Empty>
{:else}
  <PageHeader title={rt.name} back={{ href: '/runtimes', label: 'Runtimes' }}>
    {#snippet meta()}
      <Tip text={(status.compatible ? rt.requirements : status.unmet).join(' · ')}>
        {#if !status.compatible}
          <State tone="bad" label="Incompatible" />
        {:else if installs.length}
          <State tone="ok" label="Installed" />
        {:else}
          <State tone="neutral" label="Not installed" />
        {/if}
      </Tip>
      <span class="font-mono text-xs">{rt.formats.join(' ')}</span>
      <span>{apiName[rt.api]}</span>
      {#each tasks as task (task.id)}<TaskChip {task} />{/each}
    {/snippet}
    {#snippet below()}
      {#if status.unmet.length}<div class="note note-warn mb-4">{status.unmet.join(' · ')}</div>{/if}
      <Tabs {tabs} bind:value={tab.value} />
    {/snippet}
  </PageHeader>

  {#if tab.value === 'installs'}
    <div class="flex flex-col gap-8">
      {#if installs.length}
        <Installs {installs} />
      {:else if !options.length}
        <Empty icon={Package} title="Not installed" />
      {/if}
      {#if options.length}<Installers runtime={rt} {options} />{/if}
    </div>
  {:else if tab.value === 'builds'}
    <Builds {builds} />
  {:else}
    <Params params={rt.params} />
  {/if}
{/if}
