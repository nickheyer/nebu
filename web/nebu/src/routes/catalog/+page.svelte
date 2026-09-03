<script lang="ts">
  import { api, message } from '$lib/api';
  import { live } from '$lib/state.svelte';
  import { enumName, human, count, ago } from '$lib/format';
  import Badge from '$lib/components/Badge.svelte';
  import TaskLog from '$lib/components/TaskLog.svelte';
  import type { SearchHit, Source } from '$proto/source_pb';
  import type { InspectResponse } from '$proto/estimate_pb';
  import { FitVerdict, type MemoryPlan } from '$proto/estimate_pb';
  import { PoolKind } from '$proto/host_pb';

  let sources = $state<Source[]>([]);
  let sourceId = $state('');
  let query = $state('');
  let hits = $state<SearchHit[]>([]);
  let selected = $state('');
  let inspect = $state<InspectResponse | null>(null);
  let slotId = $state('');
  let busy = $state(false);
  let error = $state('');
  let tasks = $state<string[]>([]);

  $effect(() => {
    api.sources.listSources({}).then((r) => {
      sources = r.sources;
      if (!sourceId && sources.length) sourceId = sources[0].id;
    });
  });

  async function search() {
    error = '';
    busy = true;
    try {
      const resp = await api.sources.search({ sourceId, query, limit: 30 });
      hits = resp.hits;
    } catch (err) {
      error = message(err);
    } finally {
      busy = false;
    }
  }

  async function open(repo: string) {
    selected = repo;
    inspect = null;
    error = '';
    busy = true;
    try {
      inspect = await api.estimate.inspect({ sourceId, repo, slotId });
    } catch (err) {
      error = message(err);
    } finally {
      busy = false;
    }
  }

  async function pull(group: string) {
    error = '';
    try {
      const resp = await api.store.pull({ sourceId, repo: selected, group });
      if (resp.task) tasks = [resp.task.id, ...tasks];
    } catch (err) {
      error = message(err);
    }
  }

  async function watch() {
    error = '';
    try {
      await api.monitor.addWatch({ sourceId, repo: selected });
    } catch (err) {
      error = message(err);
    }
  }

  function usage(plan: MemoryPlan | undefined, kind: PoolKind): string {
    if (!plan) return '-';
    let used = 0n;
    let cap = 0n;
    for (const p of plan.pools) {
      if (p.kind === kind || (kind === PoolKind.DEVICE && p.kind === PoolKind.UNIFIED)) {
        used += p.usedBytes;
        cap += p.capacityBytes;
      }
    }
    return cap ? `${human(used)} / ${human(cap)}` : '-';
  }

  function stored(group: string): boolean {
    return [...live.models.values()].some((m) => m.sourceId === sourceId && m.repo === selected && m.group === group);
  }
</script>

<div class="space-y-4">
  <h1 class="h1">Catalog</h1>
  <form class="flex gap-2" onsubmit={(e) => { e.preventDefault(); search(); }}>
    <select class="input w-44" bind:value={sourceId}>
      {#each sources as s (s.id)}<option value={s.id}>{s.id}</option>{/each}
    </select>
    <input class="input" bind:value={query} placeholder="search models, for example qwen3 gguf" />
    <select class="input w-52" bind:value={slotId} title="plan fits inside a slot's budget">
      <option value="">plan on the whole host</option>
      {#each [...live.slots.values()] as s (s.id)}<option value={s.id}>fit in slot {s.name}</option>{/each}
    </select>
    <button class="btn btn-primary" disabled={busy}>search</button>
  </form>
  {#if error}<div class="text-sm text-red-300">{error}</div>{/if}

  <div class="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,2fr)_minmax(0,3fr)]">
    <div class="card overflow-auto">
      <table class="table">
        <thead><tr><th>repo</th><th>downloads</th><th>likes</th><th>updated</th></tr></thead>
        <tbody>
          {#each hits as h (h.repo)}
            <tr class="cursor-pointer hover:bg-zinc-800/60 {selected === h.repo ? 'bg-zinc-800' : ''}" onclick={() => open(h.repo)}>
              <td class="mono">{h.repo}</td>
              <td>{count(h.downloads)}</td>
              <td>{count(h.likes)}</td>
              <td class="muted">{ago(h.updatedAt)}</td>
            </tr>
          {:else}
            <tr><td colspan="4" class="muted">search a source, or type a repo below</td></tr>
          {/each}
        </tbody>
      </table>
      <form class="mt-3 flex gap-2" onsubmit={(e) => { e.preventDefault(); if (query.includes('/')) open(query); }}>
        <input class="input" bind:value={query} placeholder="org/repo to inspect directly" />
        <button class="btn">inspect</button>
      </form>
    </div>

    <div class="card">
      {#if busy && !inspect}
        <div class="muted">resolving and reading headers</div>
      {:else if inspect}
        <div class="mb-3 flex items-center justify-between">
          <div>
            <div class="font-semibold">{inspect.model?.repo}</div>
            <div class="muted mono text-xs">{inspect.model?.revision} {inspect.model?.commit?.slice(0, 12)}</div>
          </div>
          <button class="btn" onclick={watch}>watch for changes</button>
        </div>
        <table class="table mb-4">
          <thead><tr><th>group</th><th>arch</th><th>params</th><th>bpw</th><th>weights</th><th></th></tr></thead>
          <tbody>
            {#each inspect.descriptors as d (d.group)}
              <tr>
                <td class="mono">{d.group}</td>
                <td>{d.architecture}</td>
                <td>{count(d.parameterCount)}</td>
                <td>{d.bitsPerWeight.toFixed(2)}</td>
                <td>{human(d.totalBytes)}</td>
                <td class="text-right">
                  {#if stored(d.group)}
                    <span class="badge bg-emerald-900/70 text-emerald-200">stored</span>
                  {:else}
                    <button class="btn" onclick={() => pull(d.group)}>pull</button>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
        <table class="table">
          <thead><tr><th>group</th><th>runtime</th><th>ctx</th><th>verdict</th><th>device</th><th>host</th><th>cache</th></tr></thead>
          <tbody>
            {#each inspect.rows as r, i (i)}
              <tr>
                <td class="mono">{r.group}</td>
                <td>{r.runtimeId}</td>
                <td>{r.context}</td>
                <td><Badge state={enumName(FitVerdict, r.plan?.verdict ?? 0) === 'fits' ? 'ok' : enumName(FitVerdict, r.plan?.verdict ?? 0) === 'no' ? 'failed' : 'warn'} /> <span class="muted text-xs">{r.plan?.detail}</span></td>
                <td>{usage(r.plan, PoolKind.DEVICE)}</td>
                <td>{usage(r.plan, PoolKind.HOST)}</td>
                <td>{human(r.plan?.cacheBytes)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
        {#each inspect.warnings as w, i (i)}<div class="mt-2 text-xs text-amber-300">{w}</div>{/each}
      {:else}
        <div class="muted">pick a repository to see its weight groups and whether they fit</div>
      {/if}
    </div>
  </div>
  {#each tasks as id (id)}
    <div class="card"><TaskLog {id} /></div>
  {/each}
</div>
