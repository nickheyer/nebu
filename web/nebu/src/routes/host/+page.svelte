<script lang="ts">
  import { api, message } from '$lib/api';
  import { live, refreshHost } from '$lib/state.svelte';
  import { enumName, human } from '$lib/format';
  import Badge from '$lib/components/Badge.svelte';
  import { CheckStatus, DeviceKind, PoolKind, ProbeStatus, type DoctorReport } from '$proto/host_pb';

  let report = $state<DoctorReport | null>(null);
  let busy = $state(false);
  let error = $state('');

  async function doctor() {
    busy = true;
    error = '';
    try {
      report = (await api.host.doctor({})).report ?? null;
      if (report?.profile) live.host = report.profile;
    } catch (err) {
      error = message(err);
    } finally {
      busy = false;
    }
  }
</script>

<div class="space-y-4">
  <div class="flex items-center gap-3">
    <h1 class="h1">{live.host?.hostname} <span class="muted text-sm">{live.host?.os}/{live.host?.arch}</span></h1>
    <div class="ml-auto flex gap-2">
      <button class="btn" onclick={() => refreshHost(true)}>probe again</button>
      <button class="btn btn-primary" onclick={doctor} disabled={busy}>run doctor</button>
    </div>
  </div>
  {#if error}<div class="text-sm text-red-300">{error}</div>{/if}

  {#if report}
    <div class="card overflow-auto">
      <h2 class="h2 mb-2">Doctor</h2>
      <table class="table">
        <thead><tr><th>status</th><th>check</th><th>summary</th><th>hint</th></tr></thead>
        <tbody>
          {#each report.checks as c (c.id)}
            <tr><td><Badge state={enumName(CheckStatus, c.status)} /></td><td class="mono">{c.id}</td><td>{c.summary}</td><td class="muted">{c.hint}</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}

  <div class="card overflow-auto">
    <h2 class="h2 mb-2">Devices</h2>
    <table class="table">
      <thead><tr><th>id</th><th>kind</th><th>vendor</th><th>name</th><th>memory</th><th>facts</th></tr></thead>
      <tbody>
        {#each live.host?.devices ?? [] as d (d.id)}
          <tr><td class="mono">{d.id}</td><td>{enumName(DeviceKind, d.kind)}</td><td>{d.vendor}</td><td>{d.name}</td><td>{human(d.memoryTotalBytes)}</td><td class="muted text-xs">{Object.entries(d.facts).map(([k, v]) => `${k}=${v}`).join(' ').slice(0, 120)}</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
  <div class="card overflow-auto">
    <h2 class="h2 mb-2">Memory pools</h2>
    <table class="table">
      <thead><tr><th>id</th><th>kind</th><th>device</th><th>total</th><th>free</th></tr></thead>
      <tbody>
        {#each live.host?.pools ?? [] as p (p.id)}
          <tr><td class="mono">{p.id}</td><td>{enumName(PoolKind, p.kind)}</td><td class="mono">{p.deviceId}</td><td>{human(p.totalBytes)}</td><td>{human(p.freeBytes)}</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
  <div class="card overflow-auto">
    <h2 class="h2 mb-2">Storage</h2>
    <table class="table">
      <thead><tr><th>mount</th><th>fs</th><th>total</th><th>free</th><th>used by</th></tr></thead>
      <tbody>
        {#each live.host?.storage ?? [] as s (s.path)}
          <tr><td class="mono">{s.path}</td><td>{s.filesystem}</td><td>{human(s.totalBytes)}</td><td>{human(s.freeBytes)}</td><td class="muted text-xs">{s.uses.join(' ')}</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
  <div class="card overflow-auto">
    <h2 class="h2 mb-2">Probes</h2>
    <table class="table">
      <thead><tr><th>probe</th><th>status</th><th>detail</th></tr></thead>
      <tbody>
        {#each live.host?.probes ?? [] as p (p.probeId)}
          <tr><td>{p.probeId}</td><td><Badge state={enumName(ProbeStatus, p.status)} /></td><td class="muted">{p.detail}</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
</div>
