<script lang="ts">
  import { live, clock, activeTasks, liveInstances, unackedFindings, refreshHost, slotName } from '$lib/state.svelte';
  import { swapSlot, editSlot, newSlot, deleteSlot } from '$lib/slotActions.svelte';
  import { ago, bytes, enumLabel, duration, newestFirst, pct } from '$lib/format';
  import { attempt } from '$lib/toast.svelte';
  import { DeviceKind, PoolKind } from '$proto/host_pb';
  import { InstanceState } from '$proto/instance_pb';
  import { FindingKind } from '$proto/monitor_pb';
  import { TaskState } from '$proto/task_pb';
  import { Plus, RefreshCw, LayoutGrid, Boxes, ArrowRight, Radar, MemoryStick } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Panel from '$lib/components/ui/Panel.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Meter from '$lib/components/ui/Meter.svelte';
  import Progress from '$lib/components/ui/Progress.svelte';
  import StateBadge from '$lib/components/ui/StateBadge.svelte';
  import Badge from '$lib/components/ui/Badge.svelte';
  import Skeleton from '$lib/components/ui/Skeleton.svelte';
  import DeviceMeter from '$lib/components/DeviceMeter.svelte';
  import SlotCard from '$lib/components/SlotCard.svelte';
  import SlotDrawer from '$lib/components/SlotDrawer.svelte';
  import InstanceDrawer from '$lib/components/InstanceDrawer.svelte';
  import Onboarding from '$lib/components/Onboarding.svelte';

  let probing = $state(false);
  let slotId = $state('');
  let instanceId = $state('');

  const devices = $derived((live.host?.devices ?? []).filter((d) => d.kind !== DeviceKind.CPU));
  const hostPool = $derived((live.host?.pools ?? []).find((p) => p.kind === PoolKind.HOST));
  const slots = $derived([...live.slots.values()].sort((a, b) => a.name.localeCompare(b.name)));
  const running = $derived(liveInstances());
  const tasks = $derived(activeTasks());
  const recent = $derived([...live.tasks.values()].sort(newestFirst).slice(0, 6));
  const findings = $derived(unackedFindings().slice(0, 5));

  function poolOf(deviceId: string) {
    return live.host?.pools.find((p) => p.deviceId === deviceId && p.kind !== PoolKind.HOST);
  }

  async function probe() {
    probing = true;
    await attempt('Probe failed', () => refreshHost(true));
    probing = false;
  }
</script>

<PageHeader title={live.host?.hostname || 'Overview'} description="What this host is serving right now">
  {#snippet meta()}
    {#if live.host}
      <span class="font-mono">{live.host.os}/{live.host.arch}</span>
      <span>·</span>
      <span>{devices.length} {devices.length === 1 ? 'accelerator' : 'accelerators'}</span>
      <span>·</span>
      <span>probed {ago(live.host.probedAt, clock.now)}</span>
    {/if}
  {/snippet}
  <Button variant="outline" icon={RefreshCw} loading={probing} onclick={probe}>Probe again</Button>
  <Button variant="primary" icon={Plus} onclick={newSlot}>New slot</Button>
</PageHeader>

<div class="flex flex-col gap-6">
  <Onboarding />

  <section>
    {#if !live.ready && !live.host}
      <div class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3">
        {#each [1, 2, 3] as i (i)}<div class="panel p-4"><Skeleton rows={3} /></div>{/each}
      </div>
    {:else}
      <div class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
        {#each devices as d (d.id)}
          <DeviceMeter device={d} pool={poolOf(d.id)} />
        {/each}
        {#if hostPool}
          {@const used = hostPool.totalBytes > hostPool.freeBytes ? hostPool.totalBytes - hostPool.freeBytes : 0n}
          <div class="panel flex flex-col gap-3 p-4">
            <div class="flex items-start gap-3">
              <div class="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-line bg-raised text-fg-muted"><MemoryStick size={15} /></div>
              <div class="min-w-0 flex-1">
                <div class="text-sm font-medium text-fg">Host memory</div>
                <div class="text-xs text-fg-faint">system RAM, spill target for partial fits</div>
              </div>
              <div class="text-right">
                <div class="text-sm font-semibold tabular-nums text-fg">{pct(used, hostPool.totalBytes).toFixed(0)}%</div>
                <div class="text-[11px] text-fg-faint">used</div>
              </div>
            </div>
            <Meter value={used} max={hostPool.totalBytes} height="lg" />
            <div class="flex items-center justify-between text-xs tabular-nums">
              <span class="text-fg-muted">{bytes(used)} used</span>
              <span class="text-fg-faint">{bytes(hostPool.freeBytes)} free of {bytes(hostPool.totalBytes)}</span>
            </div>
          </div>
        {/if}
        {#if devices.length === 0 && !hostPool}
          <div class="panel md:col-span-2 xl:col-span-3">
            <Empty compact title="No devices probed" description="Nothing answered the vendor probes. The host page shows which ones ran and what they said.">
              <Button size="sm" href="/host">Open host</Button>
            </Empty>
          </div>
        {/if}
      </div>
    {/if}
  </section>

  <section>
    <div class="mb-3 flex items-center gap-3">
      <h2 class="text-sm font-semibold text-fg">Slots</h2>
      <span class="text-xs text-fg-faint">public names your router points at</span>
      <a href="/slots" class="ml-auto inline-flex items-center gap-1 text-xs text-fg-muted hover:text-fg">Manage <ArrowRight size={12} /></a>
    </div>
    {#if slots.length}
      <div class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
        {#each slots as s (s.id)}
          <SlotCard slot={s} onOpen={(x) => (slotId = x.id)} onSwap={swapSlot} onEdit={editSlot} onDelete={deleteSlot} />
        {/each}
      </div>
    {:else}
      <div class="panel">
        <Empty compact icon={LayoutGrid} title="No slots yet" description="A slot reserves devices and memory under a name that keeps answering across swaps and restarts.">
          <Button size="sm" variant="primary" icon={Plus} onclick={newSlot}>Create a slot</Button>
        </Empty>
      </div>
    {/if}
  </section>

  <section class="grid grid-cols-1 gap-4 xl:grid-cols-5">
    <Panel title="Running" description="{running.length} live" flush class="xl:col-span-3">
      {#snippet actions()}
        <a href="/instances" class="inline-flex items-center gap-1 text-xs text-fg-muted hover:text-fg">All instances <ArrowRight size={12} /></a>
      {/snippet}
      {#if running.length === 0}
        <Empty compact icon={Boxes} title="Nothing running" description="Run a stored model or drop one on a slot." />
      {:else}
        <table class="tbl">
          <thead><tr><th>name</th><th>state</th><th>model</th><th>runtime</th><th>slot</th><th class="num">up</th></tr></thead>
          <tbody>
            {#each running as i (i.id)}
              <tr class="row-link" onclick={() => (instanceId = i.id)}>
                <td class="font-medium text-fg">{i.name}</td>
                <td><StateBadge values={InstanceState} value={i.state} size="xs" /></td>
                <td class="max-w-[16rem] truncate font-mono text-xs text-fg-muted" title="{i.repo} {i.group}">{i.repo} <span class="text-fg-faint">· {i.group}</span></td>
                <td class="text-xs">{i.runtimeId}</td>
                <td class="text-xs">{i.slotId ? slotName(i.slotId) : '–'}</td>
                <td class="num text-xs text-fg-muted">{i.readyAt ? duration(i.readyAt, undefined, clock.now) : enumLabel(InstanceState, i.state)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </Panel>

    <div class="flex flex-col gap-4 xl:col-span-2">
      <Panel title="Activity" description={tasks.length ? `${tasks.length} running` : 'idle'} flush>
        {#snippet actions()}
          <a href="/tasks" class="inline-flex items-center gap-1 text-xs text-fg-muted hover:text-fg">All tasks <ArrowRight size={12} /></a>
        {/snippet}
        {#if recent.length === 0}
          <Empty compact title="No tasks yet" description="Pulls, builds, swaps, and checks show up here." />
        {:else}
          <ul class="divide-y divide-line/60">
            {#each recent as t (t.id)}
              {@const active = tasks.includes(t)}
              <li>
                <a href="/tasks?id={t.id}" class="block px-4 py-2.5 transition-colors hover:bg-raised/50">
                  <div class="flex items-center gap-2">
                    <span class="truncate text-sm text-fg">{t.title}</span>
                    <span class="ml-auto shrink-0 text-[11px] text-fg-faint">{ago(t.createdAt, clock.now)}</span>
                  </div>
                  {#if active}
                    <Progress done={t.progress?.done} total={t.progress?.total} active class="mt-2" />
                    {#if t.progress?.message}<div class="mt-1 truncate text-[11px] text-fg-muted">{t.progress.message}</div>{/if}
                  {:else}
                    <div class="mt-1 flex items-center gap-2 text-[11px] text-fg-muted">
                      <StateBadge values={TaskState} value={t.state} size="xs" />
                      {#if t.error}<span class="truncate text-bad">{t.error}</span>{/if}
                    </div>
                  {/if}
                </a>
              </li>
            {/each}
          </ul>
        {/if}
      </Panel>

      {#if findings.length}
        <Panel title="Findings" description="unacknowledged changes on watched repos" flush>
          {#snippet actions()}
            <a href="/monitor" class="inline-flex items-center gap-1 text-xs text-fg-muted hover:text-fg">Monitor <ArrowRight size={12} /></a>
          {/snippet}
          <ul class="divide-y divide-line/60">
            {#each findings as f (f.id)}
              <li class="flex items-center gap-3 px-4 py-2.5">
                <Radar size={14} class="shrink-0 text-accent" />
                <div class="min-w-0 flex-1">
                  <div class="truncate font-mono text-xs text-fg">{f.repo}{f.group ? ` · ${f.group}` : ''}</div>
                  <div class="truncate text-[11px] text-fg-muted">{f.detail}</div>
                </div>
                <Badge tone="accent" size="xs" label={enumLabel(FindingKind, f.kind)} />
              </li>
            {/each}
          </ul>
        </Panel>
      {/if}
    </div>
  </section>
</div>

<SlotDrawer bind:id={slotId} onInstance={(i) => (instanceId = i)} />
<InstanceDrawer bind:id={instanceId} />
