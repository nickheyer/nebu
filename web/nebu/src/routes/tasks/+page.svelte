<script lang="ts">
  import { page } from '$app/state';
  import { live, byCreated } from '$lib/state.svelte';
  import { enumName, human, when } from '$lib/format';
  import Badge from '$lib/components/Badge.svelte';
  import TaskLog from '$lib/components/TaskLog.svelte';
  import { TaskState } from '$proto/task_pb';

  let selected = $state(page.url.searchParams.get('id') ?? '');
  let activeOnly = $state(false);
  const list = $derived([...live.tasks.values()].filter((t) => !activeOnly || t.state === TaskState.RUNNING || t.state === TaskState.PENDING).sort(byCreated));
</script>

<div class="space-y-4">
  <div class="flex items-center gap-3">
    <h1 class="h1">Tasks</h1>
    <label class="ml-auto flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={activeOnly} /> active only</label>
  </div>
  <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
    <div class="card overflow-auto">
      <table class="table">
        <thead><tr><th>title</th><th>kind</th><th>state</th><th>progress</th><th>created</th></tr></thead>
        <tbody>
          {#each list as t (t.id)}
            <tr class="cursor-pointer hover:bg-zinc-800/60 {selected === t.id ? 'bg-zinc-800' : ''}" onclick={() => (selected = t.id)}>
              <td>{t.title}</td>
              <td>{t.kind}</td>
              <td><Badge state={enumName(TaskState, t.state)} /></td>
              <td class="muted text-xs">{t.progress?.total ? (t.progress.total < 1000n ? `${t.progress.done}/${t.progress.total}` : `${human(t.progress.done)} / ${human(t.progress.total)}`) : ''} {t.progress?.message ?? ''}</td>
              <td class="muted">{when(t.createdAt)}</td>
            </tr>
          {:else}
            <tr><td colspan="5" class="muted">no tasks</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
    <div class="card">
      {#if selected}
        {#key selected}<TaskLog id={selected} />{/key}
      {:else}
        <div class="muted">pick a task to follow its log</div>
      {/if}
    </div>
  </div>
</div>
