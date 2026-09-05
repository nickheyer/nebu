<script lang="ts">
  import { live, clock, activeTasks, liveInstances, unackedFindings, probeHost, slotName, hostName } from '$lib/state.svelte';
  import { newSlot } from '$lib/slotActions.svelte';
  import { ago, byName, enumLabel, duration, newestFirst } from '$lib/format';
  import { DeviceKind } from '$proto/host_pb';
  import { InstanceState } from '$proto/instance_pb';
  import { FindingKind } from '$proto/monitor_pb';
  import { TaskState } from '$proto/task_pb';
  import { Plus, RefreshCw, Radar, ChevronRight } from '@lucide/svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Panel from '$lib/components/ui/Panel.svelte';
  import Progress from '$lib/components/ui/Progress.svelte';
  import StateBadge from '$lib/components/ui/StateBadge.svelte';
  import Skeleton from '$lib/components/ui/Skeleton.svelte';
  import Tip from '$lib/components/ui/Tip.svelte';
  import MemoryRows from '$lib/components/MemoryRows.svelte';
  import SlotBay from '$lib/components/SlotBay.svelte';
  import SlotDrawer from '$lib/components/SlotDrawer.svelte';
  import InstanceDrawer from '$lib/components/InstanceDrawer.svelte';
  import Setup from '$lib/components/Setup.svelte';

  let probing = $state(false);
  let slotId = $state('');
  let instanceId = $state('');

  const name = $derived(hostName());
  const labeled = $derived(!!live.settings?.hostLabel && live.settings.hostLabel !== live.host?.hostname);
  const accelerators = $derived((live.host?.devices ?? []).filter((d) => d.kind !== DeviceKind.CPU).length);
  const slots = $derived([...live.slots.values()].sort(byName((s) => s.name)));
  const running = $derived(liveInstances());
  const tasks = $derived(activeTasks());
  const recent = $derived([...live.tasks.values()].sort(newestFirst((t) => t.createdAt)).slice(0, 6));
  const findings = $derived(unackedFindings().slice(0, 5));

  async function probe() {
    probing = true;
    await probeHost();
    probing = false;
  }
</script>

<header class="mb-6 flex flex-wrap items-end gap-4">
  <div class="min-w-0">
    <h1 class="truncate text-xl font-semibold tracking-tight text-fg">{name || 'Overview'}</h1>
    <div class="mt-1 flex flex-wrap items-center gap-2 text-xs text-fg-muted">
      {#if live.host}
        {#if labeled}<span class="font-mono text-fg-faint">{live.host.hostname}</span><span>·</span>{/if}
        <span class="font-mono">{live.host.os}/{live.host.arch}</span>
        <span>·</span>
        <span>{accelerators} {accelerators === 1 ? 'accelerator' : 'accelerators'}</span>
        <span>·</span>
        <span>probed {ago(live.host.probedAt, clock.now)}</span>
      {/if}
    </div>
  </div>
  <div class="ml-auto flex shrink-0 items-center gap-2">
    <Tip text="Probe the hardware again">
      <Button variant="outline" icon={RefreshCw} loading={probing} onclick={probe} aria-label="Probe the hardware again" />
    </Tip>
  </div>
</header>

<div class="flex flex-col gap-7">
  <Setup />

  <section>
    <div class="mb-2 flex items-center gap-3">
      <a href="/host" class="group inline-flex items-center gap-1 text-sm font-semibold text-fg hover:text-accent"><h2>Memory</h2><ChevronRight size={14} class="text-fg-faint transition-colors group-hover:text-accent" /></a>
    </div>
    {#if !live.ready && !live.host}
      <div class="rounded-lg border border-line p-4"><Skeleton rows={3} /></div>
    {:else if live.host}
      <MemoryRows host={live.host} />
    {/if}
  </section>

  <section>
    <div class="mb-2 flex items-center gap-3">
      <a href="/slots" class="group inline-flex items-center gap-1 text-sm font-semibold text-fg hover:text-accent"><h2>Slots</h2><ChevronRight size={14} class="text-fg-faint transition-colors group-hover:text-accent" /></a>
      <span class="text-xs text-fg-faint">{slots.length ? `${slots.filter((s) => live.routes.get(s.name)?.state === 2).length} of ${slots.length} serving` : ''}</span>
      <Button size="sm" variant="primary" icon={Plus} class="ml-auto" onclick={newSlot}>New slot</Button>
    </div>
    <div class="flex flex-col gap-2">
      {#each slots as s (s.id)}
        <SlotBay slot={s} onOpen={(x) => (slotId = x.id)} />
      {:else}
        <div class="flex h-16 items-center justify-center rounded-lg border border-dashed border-line text-sm text-fg-faint">No slots</div>
      {/each}
    </div>
  </section>

  <section class="grid grid-cols-1 gap-5 xl:grid-cols-5">
    <Panel title="Running" href="/instances" description={running.length ? `${running.length}` : ''} flush class="xl:col-span-3">
      {#if running.length === 0}
        <div class="px-4 py-8 text-center text-sm text-fg-faint">Nothing running</div>
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

    <div class="flex flex-col gap-5 xl:col-span-2">
      <Panel title="Activity" href="/tasks" description={tasks.length ? `${tasks.length} running` : ''} flush>
        {#if recent.length === 0}
          <div class="px-4 py-8 text-center text-sm text-fg-faint">No tasks yet</div>
        {:else}
          <ol class="divide-y divide-line/60">
            {#each recent as t (t.id)}
              {@const active = tasks.includes(t)}
              <li>
                <a href="/tasks?id={t.id}" class="flex gap-3 px-4 py-2.5 transition-colors hover:bg-raised/50">
                  <span class="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full {active ? 'bg-accent pulse relative' : t.state === TaskState.FAILED ? 'bg-bad' : t.state === TaskState.SUCCEEDED ? 'bg-ok' : 'bg-line-strong'}"></span>
                  <span class="min-w-0 flex-1">
                    <span class="flex items-center gap-2">
                      <span class="truncate text-sm text-fg">{t.title}</span>
                      <span class="ml-auto shrink-0 text-[11px] text-fg-faint">{ago(t.createdAt, clock.now)}</span>
                    </span>
                    {#if active}
                      <Progress done={t.progress?.done} total={t.progress?.total} active class="mt-2" />
                      {#if t.progress?.message}<span class="mt-1 block truncate text-[11px] text-fg-muted">{t.progress.message}</span>{/if}
                    {:else if t.error}
                      <span class="mt-0.5 block truncate text-[11px] text-bad">{t.error}</span>
                    {:else}
                      <span class="mt-0.5 block text-[11px] text-fg-faint">{enumLabel(TaskState, t.state)}</span>
                    {/if}
                  </span>
                </a>
              </li>
            {/each}
          </ol>
        {/if}
      </Panel>

      {#if findings.length}
        <Panel title="Findings" href="/monitor" description="{findings.length} new" flush>
          <ul class="divide-y divide-line/60">
            {#each findings as f (f.id)}
              <li class="flex items-center gap-3 px-4 py-2.5">
                <Radar size={14} class="shrink-0 text-accent" />
                <div class="min-w-0 flex-1">
                  <div class="truncate font-mono text-xs text-fg">{f.repo}{f.group ? ` · ${f.group}` : ''}</div>
                  <div class="truncate text-[11px] text-fg-muted">{f.detail}</div>
                </div>
                <span class="shrink-0 text-[11px] text-fg-faint">{enumLabel(FindingKind, f.kind)}</span>
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
