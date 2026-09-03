<script lang="ts">
  import { api, message } from '$lib/api';
  import { live, byCreated } from '$lib/state.svelte';
  import { enumName, human, when } from '$lib/format';
  import Badge from '$lib/components/Badge.svelte';
  import InstanceLog from '$lib/components/InstanceLog.svelte';
  import { InstanceState, type Instance } from '$proto/instance_pb';

  let all = $state(false);
  let selected = $state('');
  let error = $state('');

  const list = $derived([...live.instances.values()].filter((i) => all || (i.state !== InstanceState.STOPPED && i.state !== InstanceState.FAILED)).sort(byCreated));
  const current = $derived(selected ? live.instances.get(selected) : undefined);

  function device(i: Instance): string {
    const m = i.measurements.find((x) => x.key === 'device.used');
    if (m) return human(m.bytes);
    const planned = i.plan?.pools.filter((p) => p.kind === 1 || p.kind === 3).reduce((a, p) => a + p.usedBytes, 0n);
    return planned ? '~' + human(planned) : '-';
  }

  async function stop(id: string) {
    error = '';
    try {
      await api.instances.stopInstance({ id });
    } catch (err) {
      error = message(err);
    }
  }
</script>

<div class="space-y-4">
  <div class="flex items-center gap-3">
    <h1 class="h1">Instances</h1>
    <label class="ml-auto flex items-center gap-2 text-sm"><input type="checkbox" bind:checked={all} /> include stopped</label>
  </div>
  {#if error}<div class="text-sm text-red-300">{error}</div>{/if}
  <div class="card overflow-auto">
    <table class="table">
      <thead><tr><th>name</th><th>state</th><th>model</th><th>runtime</th><th>pid</th><th>endpoint</th><th>device</th><th>slot</th><th>detail</th><th></th></tr></thead>
      <tbody>
        {#each list as i (i.id)}
          <tr class="cursor-pointer hover:bg-zinc-800/60 {selected === i.id ? 'bg-zinc-800' : ''}" onclick={() => (selected = i.id)}>
            <td>{i.name}</td>
            <td><Badge state={enumName(InstanceState, i.state)} /></td>
            <td class="mono">{i.repo}:{i.group}</td>
            <td>{i.runtimeId}</td>
            <td>{i.pid}</td>
            <td class="mono">{i.endpoint}</td>
            <td>{device(i)}</td>
            <td class="mono">{i.slotId ? live.slots.get(i.slotId)?.name ?? i.slotId : ''}</td>
            <td class="text-xs text-amber-300">{i.triage[0]?.summary ?? i.error}</td>
            <td class="text-right">
              {#if i.state !== InstanceState.STOPPED && i.state !== InstanceState.FAILED}
                <button class="btn btn-danger" onclick={(e) => { e.stopPropagation(); stop(i.id); }}>stop</button>
              {/if}
            </td>
          </tr>
        {:else}
          <tr><td colspan="10" class="muted">nothing here</td></tr>
        {/each}
      </tbody>
    </table>
  </div>

  {#if current}
    <div class="card space-y-3">
      <div class="flex flex-wrap items-center gap-2">
        <h2 class="h2">{current.name}</h2>
        <span class="muted mono text-xs">{current.id}</span>
        <Badge state={enumName(InstanceState, current.state)} />
        <span class="muted text-xs">created {when(current.createdAt)}, ready {when(current.readyAt)}, stopped {when(current.stoppedAt)}</span>
      </div>
      {#if current.plan}
        <div class="text-sm">plan {enumName({ 0: 'UNSPECIFIED', 1: 'FITS', 2: 'PARTIAL', 3: 'NO' }, current.plan.verdict)}, weights {human(current.plan.weightsBytes)}, cache {human(current.plan.cacheBytes)}, overhead {human(current.plan.overheadBytes)} {current.plan.detail}</div>
      {/if}
      <div class="flex flex-wrap gap-x-4 gap-y-1 text-xs">
        {#each Object.entries(current.params) as [k, v] (k)}<span><span class="muted">{k}=</span>{v}</span>{/each}
      </div>
      {#if current.measurements.length}
        <table class="table">
          <thead><tr><th>measurement</th><th>bytes</th><th>line</th></tr></thead>
          <tbody>
            {#each current.measurements as m (m.key)}
              <tr><td>{m.key}</td><td>{human(m.bytes)}</td><td class="muted text-xs">{m.line}</td></tr>
            {/each}
          </tbody>
        </table>
      {/if}
      {#each current.triage as hit (hit.id)}
        <div class="rounded border border-amber-900 bg-amber-950/40 p-2 text-sm">
          <div class="font-medium">{hit.summary}</div>
          <div class="muted">{hit.hint}</div>
          {#if Object.keys(hit.fix).length}<div class="text-xs">try {Object.entries(hit.fix).map(([k, v]) => `${k}=${v}`).join(' ')}</div>{/if}
          <div class="mono muted mt-1">{hit.line}</div>
        </div>
      {/each}
      <div class="mono muted text-xs">{current.command.join(' ')}</div>
      <InstanceLog id={current.id} follow={current.state !== InstanceState.STOPPED && current.state !== InstanceState.FAILED} />
    </div>
  {/if}
</div>
