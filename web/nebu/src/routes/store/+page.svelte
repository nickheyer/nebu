<script lang="ts">
  import { api, message } from '$lib/api';
  import { live } from '$lib/state.svelte';
  import { human, when } from '$lib/format';
  import RunDialog from '$lib/components/RunDialog.svelte';
  import TaskLog from '$lib/components/TaskLog.svelte';
  import type { StoredModel, StoreStatus } from '$proto/store_pb';

  let status = $state<StoreStatus | null>(null);
  let error = $state('');
  let runOpen = $state(false);
  let target = $state<StoredModel | null>(null);
  let tasks = $state<string[]>([]);
  let exportDir = $state('');

  const models = $derived([...live.models.values()].sort((a, b) => (a.repo + a.group).localeCompare(b.repo + b.group)));

  async function refresh() {
    try {
      status = (await api.store.getStatus({})).status ?? null;
    } catch (err) {
      error = message(err);
    }
  }
  $effect(() => {
    refresh();
  });

  function key(m: StoredModel) {
    return `${m.sourceId}/${m.repo}/${m.group}`;
  }

  async function remove(m: StoredModel) {
    if (!confirm(`remove ${m.repo} ${m.group} from the store?`)) return;
    error = '';
    try {
      await api.store.removeModel({ sourceId: m.sourceId, repo: m.repo, group: m.group, gc: true });
      live.models.delete(key(m));
      refresh();
    } catch (err) {
      error = message(err);
    }
  }

  async function gc() {
    error = '';
    try {
      const r = await api.store.gc({ partials: true });
      error = `collected ${r.removed} files, freed ${human(r.freedBytes)}`;
      refresh();
    } catch (err) {
      error = message(err);
    }
  }

  async function verify() {
    error = '';
    try {
      const r = await api.store.verify({});
      if (r.task) tasks = [r.task.id, ...tasks];
    } catch (err) {
      error = message(err);
    }
  }

  async function exportAll() {
    if (!exportDir) return;
    error = '';
    try {
      const r = await api.store.export({ dir: exportDir });
      if (r.task) tasks = [r.task.id, ...tasks];
    } catch (err) {
      error = message(err);
    }
  }

  function drag(ev: DragEvent, m: StoredModel) {
    ev.dataTransfer?.setData('text/nebu-model', key(m));
    ev.dataTransfer!.effectAllowed = 'move';
  }
</script>

<div class="space-y-4">
  <div class="flex flex-wrap items-center gap-3">
    <h1 class="h1">Store</h1>
    {#if status}
      <span class="muted text-sm">{status.models.toString()} models, {status.blobs.toString()} blobs, {human(status.blobBytes)}{status.partials ? `, ${status.partials} partials holding ${human(status.partialBytes)}` : ''}</span>
      <span class="muted mono text-xs">{status.path}</span>
    {/if}
    <div class="ml-auto flex gap-2">
      <button class="btn" onclick={verify}>verify</button>
      <button class="btn" onclick={gc}>gc</button>
    </div>
  </div>
  <div class="flex gap-2">
    <input class="input max-w-md" bind:value={exportDir} placeholder="/mnt/mirror to export every model as a mirror" />
    <button class="btn" onclick={exportAll} disabled={!exportDir}>export</button>
  </div>
  {#if error}<div class="text-sm text-amber-300">{error}</div>{/if}

  <div class="card overflow-auto">
    <table class="table">
      <thead><tr><th></th><th>repo</th><th>group</th><th>format</th><th>arch</th><th>size</th><th>pulled</th><th></th></tr></thead>
      <tbody>
        {#each models as m (key(m))}
          <tr draggable="true" ondragstart={(e) => drag(e, m)} class="cursor-grab">
            <td class="muted">::</td>
            <td class="mono">{m.repo}</td>
            <td class="mono">{m.group}</td>
            <td>{m.formatId}</td>
            <td>{m.descriptor?.architecture}</td>
            <td>{human(m.bytes)}</td>
            <td class="muted">{when(m.pulledAt)}</td>
            <td class="whitespace-nowrap text-right">
              <button class="btn btn-primary" onclick={() => { target = m; runOpen = true; }}>run</button>
              <button class="btn btn-danger" onclick={() => remove(m)}>remove</button>
            </td>
          </tr>
        {:else}
          <tr><td colspan="8" class="muted">nothing stored, pull something from the catalog</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
  <p class="muted text-xs">drag a row onto a slot on the dashboard or the slots page to run or swap it there</p>
  {#each tasks as id (id)}
    <div class="card"><TaskLog {id} /></div>
  {/each}
  <RunDialog bind:open={runOpen} model={target} />
</div>
