<script lang="ts">
  import { api, message } from '$lib/api';
  import { live } from '$lib/state.svelte';
  import { enumName, parsePairs, when, ago } from '$lib/format';
  import Badge from '$lib/components/Badge.svelte';
  import TaskLog from '$lib/components/TaskLog.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import { FindingKind } from '$proto/monitor_pb';
  import type { Source } from '$proto/source_pb';

  let sources = $state<Source[]>([]);
  let addOpen = $state(false);
  let sourceId = $state('');
  let repo = $state('');
  let revision = $state('');
  let match = $state('');
  let autoPull = $state(false);
  let slotId = $state('');
  let runtimeId = $state('');
  let params = $state('');
  let error = $state('');
  let taskId = $state('');
  let showAcked = $state(false);

  const watches = $derived([...live.watches.values()].sort((a, b) => a.repo.localeCompare(b.repo)));
  const findings = $derived([...live.findings.values()].filter((f) => showAcked || !f.acknowledged).sort((a, b) => Number((b.foundAt?.seconds ?? 0n) - (a.foundAt?.seconds ?? 0n))));

  $effect(() => {
    api.sources.listSources({}).then((r) => {
      sources = r.sources;
      if (!sourceId && sources.length) sourceId = sources[0].id;
    });
    api.monitor.listFindings({}).then((r) => {
      for (const f of r.findings) live.findings.set(f.id, f);
    });
  });

  async function add() {
    error = '';
    try {
      await api.monitor.addWatch({ sourceId, repo, revision, groupMatch: match, autoPull: autoPull || !!slotId, slotId, runtimeId, params: parsePairs(params) });
      addOpen = false;
      repo = revision = match = slotId = runtimeId = params = '';
    } catch (err) {
      error = message(err);
    }
  }

  async function remove(id: string) {
    error = '';
    try {
      await api.monitor.removeWatch({ id });
    } catch (err) {
      error = message(err);
    }
  }

  async function check(id = '') {
    error = '';
    try {
      const r = await api.monitor.checkWatches({ id });
      taskId = r.task?.id ?? '';
    } catch (err) {
      error = message(err);
    }
  }

  async function ack(id: string) {
    error = '';
    try {
      await api.monitor.ackFinding({ id });
    } catch (err) {
      error = message(err);
    }
  }
</script>

<div class="space-y-4">
  <div class="flex items-center gap-3">
    <h1 class="h1">Monitor</h1>
    <div class="ml-auto flex gap-2">
      <button class="btn" onclick={() => check()} disabled={!watches.length}>check all now</button>
      <button class="btn btn-primary" onclick={() => (addOpen = true)}>watch a repo</button>
    </div>
  </div>
  {#if error}<div class="text-sm text-red-300">{error}</div>{/if}

  <div class="card overflow-auto">
    <table class="table">
      <thead><tr><th>repo</th><th>source</th><th>commit</th><th>groups</th><th>match</th><th>auto</th><th>slot</th><th>checked</th><th></th></tr></thead>
      <tbody>
        {#each watches as w (w.id)}
          <tr>
            <td class="mono">{w.repo}{w.revision ? '@' + w.revision : ''}</td>
            <td>{w.sourceId}</td>
            <td class="mono">{w.lastCommit.slice(0, 12) || '-'}</td>
            <td title={w.knownGroups.join(', ')}>{w.knownGroups.length}</td>
            <td class="mono">{w.groupMatch}</td>
            <td>{w.autoPull ? (w.slotId ? 'pull and swap' : 'pull') : '-'}</td>
            <td>{w.slotId ? live.slots.get(w.slotId)?.name ?? w.slotId : ''}</td>
            <td class="muted">{ago(w.checkedAt)}{#if w.error}<div class="text-xs text-red-300">{w.error}</div>{/if}</td>
            <td class="whitespace-nowrap text-right">
              <button class="btn" onclick={() => check(w.id)}>check</button>
              <button class="btn btn-danger" onclick={() => remove(w.id)}>remove</button>
            </td>
          </tr>
        {:else}
          <tr><td colspan="9" class="muted">nothing watched, add a repository to be told about new revisions and quants</td></tr>
        {/each}
      </tbody>
    </table>
  </div>

  {#if taskId}<div class="card"><TaskLog id={taskId} /></div>{/if}

  <div class="card overflow-auto">
    <div class="mb-2 flex items-center gap-3">
      <h2 class="h2">Findings</h2>
      <label class="ml-auto flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={showAcked} /> show acknowledged</label>
    </div>
    <table class="table">
      <thead><tr><th>kind</th><th>repo</th><th>group</th><th>detail</th><th>found</th><th>task</th><th></th></tr></thead>
      <tbody>
        {#each findings as f (f.id)}
          <tr class={f.acknowledged ? 'opacity-60' : ''}>
            <td><Badge state={enumName(FindingKind, f.kind).replace('_', ' ')} /></td>
            <td class="mono">{f.repo}</td>
            <td class="mono">{f.group}</td>
            <td>{f.detail}</td>
            <td class="muted">{when(f.foundAt)}</td>
            <td>{#if f.taskId}<a class="hover:underline" href="/tasks?id={f.taskId}">{f.taskId.slice(0, 8)}</a>{/if}</td>
            <td class="text-right">{#if !f.acknowledged}<button class="btn" onclick={() => ack(f.id)}>ack</button>{/if}</td>
          </tr>
        {:else}
          <tr><td colspan="7" class="muted">no findings</td></tr>
        {/each}
      </tbody>
    </table>
  </div>

  <Modal bind:open={addOpen} title="Watch a repository">
    <div class="grid grid-cols-2 gap-3">
      <div><label class="label" for="w-source">Source</label><select id="w-source" class="input" bind:value={sourceId}>{#each sources as s (s.id)}<option value={s.id}>{s.id}</option>{/each}</select></div>
      <div><label class="label" for="w-repo">Repo</label><input id="w-repo" class="input" bind:value={repo} placeholder="org/name" /></div>
      <div><label class="label" for="w-rev">Revision</label><input id="w-rev" class="input" bind:value={revision} placeholder="source default" /></div>
      <div><label class="label" for="w-match">Group match, regex</label><input id="w-match" class="input" bind:value={match} placeholder="Q4_K_M|Q5" /></div>
      <div class="col-span-2 flex items-center gap-2"><input id="w-auto" type="checkbox" bind:checked={autoPull} /><label for="w-auto" class="text-sm">pull matching groups when they appear or change</label></div>
      <div><label class="label" for="w-slot">Swap into slot</label><select id="w-slot" class="input" bind:value={slotId}><option value="">no swap</option>{#each [...live.slots.values()] as s (s.id)}<option value={s.id}>{s.name}</option>{/each}</select></div>
      <div><label class="label" for="w-runtime">Runtime for the swap</label><input id="w-runtime" class="input" bind:value={runtimeId} placeholder="slot default" /></div>
      <div class="col-span-2"><label class="label" for="w-params">Params for the swap</label><textarea id="w-params" class="input h-16" bind:value={params}></textarea></div>
    </div>
    <div class="mt-4 flex justify-end"><button class="btn btn-primary" onclick={add} disabled={!repo}>watch</button></div>
  </Modal>
</div>
