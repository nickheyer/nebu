<script lang="ts">
  import { page } from '$app/state';
  import { cached, live, clock, installsOf, runtimeStatus, taskActive } from '$lib/state.svelte';
  import { fit, installTask, buildsOf, taskDoing } from '$lib/runtimes';
  import { ago } from '$lib/format';
  import { TaskState } from '$proto/task_pb';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Section from '$lib/components/ui/Section.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import TaskLog from '$lib/components/TaskLog.svelte';
  import InstallForm from '$lib/components/runtimes/InstallForm.svelte';
  import InstallsTable from '$lib/components/runtimes/InstallsTable.svelte';
  import BuildsTable from '$lib/components/runtimes/BuildsTable.svelte';
  import ParamTable from '$lib/components/runtimes/ParamTable.svelte';

  const id = $derived(page.params.id ?? '');
  const status = $derived(runtimeStatus(id));
  const rt = $derived(status?.runtime);
  const loading = $derived(!cached.loaded);
  const installs = $derived(installsOf(id));
  const task = $derived(installTask(id));
  const installing = $derived(!!task && taskActive(task));
  // Builds that have not become an install: under way, failed, or left behind
  const builds = $derived(buildsOf(id).filter((b) => !b.installId || !live.installs.has(b.installId)));
  // A failed build is told by its row, so the note is for failures no row records
  const failedInBuilds = $derived(!!task && builds.some((b) => b.taskId === task.id));
  const doing = $derived(taskDoing(status, task));
</script>

{#if loading}
  <div class="skeleton h-40" aria-busy="true"></div>
{:else if !status || !rt}
  <PageHeader title="Runtime not found" back={{ href: '/runtimes', label: 'Runtimes' }} />
  <Empty title="No runtime named {id}">
    <Button href="/runtimes">Back to Runtimes</Button>
  </Empty>
{:else}
  <PageHeader title={rt.name} back={{ href: '/runtimes', label: 'Runtimes' }}>
    {#snippet meta()}
      {#if !status.compatible}<State tone={fit(status).tone} label={fit(status).label} />{/if}
    {/snippet}
  </PageHeader>

  <div class="flex flex-col gap-10">
    {#if installs.length}
      <div class="overflow-x-auto"><InstallsTable {installs} /></div>
    {/if}
    {#if installing && task}
      <Card title={doing}>
        {#key task.id}<TaskLog id={task.id} height="h-56" />{/key}
      </Card>
    {:else}
      {#if task?.state === TaskState.FAILED && !failedInBuilds}
        <div class="note note-bad flex flex-wrap items-baseline gap-x-3">
          <span class="font-medium">{doing.replace(/ing$/, '')} failed {ago(task.finishedAt ?? task.createdAt, clock.now)}</span>
          <span class="min-w-0 flex-1 truncate" title={task.error}>{task.error}</span>
          <a class="underline underline-offset-2" href="/tasks/{task.id}">Open task</a>
        </div>
      {/if}
      {#key id}<InstallForm {status} />{/key}
    {/if}
    {#if builds.length}
      <Section title="Builds" count={builds.length}>
        <div class="overflow-x-auto"><BuildsTable {builds} /></div>
      </Section>
    {/if}
    <Section title="Parameters" count={rt.params.length || undefined}>
      <ParamTable params={rt.params} />
    </Section>
  </div>
{/if}
