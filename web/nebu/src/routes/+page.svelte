<script lang="ts">
  import { live, activeTasks, byCreated } from '$lib/state.svelte';
  import { api, message } from '$lib/api';
  import { enumName, human, ago } from '$lib/format';
  import SlotCard from '$lib/components/SlotCard.svelte';
  import Badge from '$lib/components/Badge.svelte';
  import TaskLog from '$lib/components/TaskLog.svelte';
  import { DeviceKind } from '$proto/host_pb';
  import { InstanceState } from '$proto/instance_pb';
  import { TaskState } from '$proto/task_pb';
  import { goto } from '$app/navigation';

  let error = $state('');
  let taskId = $state('');
  const devices = $derived((live.host?.devices ?? []).filter((d) => d.kind !== DeviceKind.CPU));
  const pools = $derived(live.host?.pools ?? []);
  const running = $derived([...live.instances.values()].filter((i) => i.state !== InstanceState.STOPPED && i.state !== InstanceState.FAILED).sort(byCreated));
  const recent = $derived([...live.tasks.values()].sort(byCreated).slice(0, 8));

  function poolOf(deviceId: string) {
    return pools.find((p) => p.deviceId === deviceId || p.id === deviceId);
  }

  async function dropModel(slotId: string, key: string) {
    const model = live.models.get(key);
    if (!model) return;
    error = '';
    try {
      const resp = await api.slots.swap({ slotId, run: { sourceId: model.sourceId, repo: model.repo, group: model.group, slotId } });
      taskId = resp.task?.id ?? '';
    } catch (err) {
      error = message(err);
    }
  }
</script>

<div class="space-y-6">
  <div class="flex items-baseline justify-between">
    <h1 class="h1">{live.host?.hostname ?? 'host'} <span class="muted text-sm">{live.host?.os}/{live.host?.arch}</span></h1>
    <a class="btn" href="/host">details</a>
  </div>

  <section class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
    {#each devices as d (d.id)}
      {@const pool = poolOf(d.id)}
      {@const total = Number(pool?.totalBytes ?? d.memoryTotalBytes)}
      {@const free = Number(pool?.freeBytes ?? d.memoryFreeBytes)}
      <div class="card">
        <div class="flex items-center justify-between">
          <div class="font-medium">{d.name}</div>
          <span class="muted text-xs">{d.vendor} {enumName(DeviceKind, d.kind)}</span>
        </div>
        <div class="mt-2 h-2 w-full overflow-hidden rounded bg-zinc-800">
          <div class="h-full bg-emerald-600" style="width: {total ? Math.round(((total - free) / total) * 100) : 0}%"></div>
        </div>
        <div class="muted mt-1 text-xs">{human(total - free)} used of {human(total)}, {human(free)} free</div>
      </div>
    {:else}
      <div class="card muted">no devices probed, see host</div>
    {/each}
  </section>

  <section>
    <div class="mb-2 flex items-center justify-between">
      <h2 class="h2">Slots</h2>
      <a class="btn" href="/slots">manage</a>
    </div>
    {#if error}<div class="mb-2 text-sm text-red-300">{error}</div>{/if}
    <div class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
      {#each [...live.slots.values()] as slot (slot.id)}
        <SlotCard {slot} onDropModel={dropModel} onSelect={() => goto('/slots')} />
      {:else}
        <div class="card muted">no slots yet, create one on the slots page and drag a stored model onto it</div>
      {/each}
    </div>
    {#if taskId}<div class="card mt-3"><TaskLog id={taskId} /></div>{/if}
  </section>

  <section class="grid grid-cols-1 gap-4 lg:grid-cols-2">
    <div class="card">
      <h2 class="h2 mb-2">Running <span class="muted text-xs">{running.length}</span></h2>
      <table class="table">
        <thead><tr><th>name</th><th>state</th><th>model</th><th>runtime</th></tr></thead>
        <tbody>
          {#each running as i (i.id)}
            <tr>
              <td><a class="hover:underline" href="/instances">{i.name}</a></td>
              <td><Badge state={enumName(InstanceState, i.state)} /></td>
              <td class="mono">{i.repo}:{i.group}</td>
              <td>{i.runtimeId}</td>
            </tr>
          {:else}
            <tr><td colspan="4" class="muted">nothing running</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
    <div class="card">
      <h2 class="h2 mb-2">Tasks <span class="muted text-xs">{activeTasks().length} active</span></h2>
      <table class="table">
        <thead><tr><th>title</th><th>state</th><th>when</th></tr></thead>
        <tbody>
          {#each recent as t (t.id)}
            <tr>
              <td><a class="hover:underline" href="/tasks?id={t.id}">{t.title}</a></td>
              <td><Badge state={enumName(TaskState, t.state)} /></td>
              <td class="muted">{ago(t.createdAt)}</td>
            </tr>
          {:else}
            <tr><td colspan="3" class="muted">no tasks yet</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  </section>
</div>
