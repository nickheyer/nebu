<script lang="ts">
  import { api } from '$lib/api';
  import { live, clock, instanceLive, slotName } from '$lib/state.svelte';
  import { selectionParam } from '$lib/selection.svelte';
  import { launch } from '$lib/launch';
  import { bytes, duration, newestFirst, when, ago } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { InstanceState, type Instance } from '$proto/instance_pb';
  import { PoolKind } from '$proto/host_pb';
  import { Boxes, Square, RotateCcw, ScrollText } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Panel from '$lib/components/ui/Panel.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Tabs from '$lib/components/ui/Tabs.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import StateBadge from '$lib/components/ui/StateBadge.svelte';
  import Menu from '$lib/components/ui/Menu.svelte';
  import InstanceDrawer from '$lib/components/InstanceDrawer.svelte';

  let view = $state('running');
  const sel = selectionParam('/instances');

  const all = $derived([...live.instances.values()].sort(newestFirst((i) => i.createdAt)));
  const running = $derived(all.filter(instanceLive));
  const failed = $derived(all.filter((i) => i.state === InstanceState.FAILED));
  const list = $derived(view === 'running' ? running : view === 'failed' ? failed : all);

  function memory(i: Instance): string {
    const m = i.measurements.find((x) => x.key === 'device.used') ?? i.measurements.find((x) => x.key.endsWith('.used'));
    if (m) return bytes(m.bytes);
    const planned = i.plan?.pools.filter((p) => p.kind === PoolKind.DEVICE || p.kind === PoolKind.UNIFIED).reduce((a, p) => a + p.usedBytes, 0n) ?? 0n;
    return planned ? '≈ ' + bytes(planned) : '–';
  }

  async function stop(i: Instance) {
    const yes = await confirm({ title: `Stop ${i.name}?`, message: 'The process gets its grace period, then is killed. It will not relaunch after a daemon restart.', action: 'Stop', tone: 'bad' });
    if (!yes) return;
    try {
      await api.instances.stopInstance({ id: i.id });
      ok(`Stopping ${i.name}`);
    } catch (err) {
      fail(err, 'Stop failed');
    }
  }
</script>

<PageHeader title="Instances" description="Runtime processes serving stored models, live and past">
  <Tabs bind:value={view} tabs={[{ id: 'running', label: 'Running', count: running.length }, { id: 'failed', label: 'Failed', count: failed.length || undefined }, { id: 'all', label: 'All', count: all.length }]} />
</PageHeader>

<Panel flush>
  {#if list.length === 0}
    <Empty icon={Boxes} title={view === 'running' ? 'Nothing running' : view === 'failed' ? 'No failures' : 'No instances yet'} description={view === 'running' ? 'Run a stored model, or drop one on a slot.' : 'Instances stay listed after they stop so their plan, measurements, and log remain readable.'}>
      {#if view === 'running'}<Button size="sm" variant="primary" href="/store">Open store</Button>{/if}
    </Empty>
  {:else}
    <div class="overflow-x-auto">
      <table class="tbl">
        <thead>
          <tr><th>name</th><th>state</th><th>model</th><th>runtime</th><th>endpoint</th><th class="num">device</th><th>slot</th><th class="num">{view === 'running' ? 'up' : 'when'}</th><th></th></tr>
        </thead>
        <tbody>
          {#each list as i (i.id)}
            {@const alive = instanceLive(i)}
            <tr class="row-link {sel.id === i.id ? 'row-active' : ''}" onclick={() => (sel.id = i.id)}>
              <td>
                <div class="font-medium text-fg">{i.name}</div>
                {#if i.triage[0]?.summary || (!alive && i.error)}
                  <div class="max-w-xs truncate text-[11px] {i.state === InstanceState.FAILED ? 'text-bad' : 'text-fg-faint'}" title={i.triage[0]?.summary || i.error}>{i.triage[0]?.summary || i.error}</div>
                {/if}
              </td>
              <td><StateBadge values={InstanceState} value={i.state} size="xs" /></td>
              <td class="max-w-[14rem] truncate font-mono text-xs" title="{i.repo} {i.group}">{i.repo.split('/').pop()} <span class="text-fg-faint">· {i.group}</span></td>
              <td class="text-xs">{i.runtimeId}</td>
              <td class="font-mono text-xs text-fg-muted">{i.endpoint || '–'}</td>
              <td class="num text-xs">{memory(i)}</td>
              <td class="text-xs">{i.slotId ? slotName(i.slotId) : '–'}</td>
              <td class="num text-xs text-fg-muted" title={when(i.createdAt)}>{alive && i.readyAt ? duration(i.readyAt, undefined, clock.now) : ago(i.stoppedAt ?? i.createdAt, clock.now)}</td>
              <td class="text-right" onclick={(e) => e.stopPropagation()}>
                <Menu
                  items={[
                    { label: 'Open log', icon: ScrollText, onSelect: () => (sel.id = i.id) },
                    ...(alive
                      ? [{ label: 'Stop', icon: Square, tone: 'bad' as const, onSelect: () => stop(i) }]
                      : [{ label: 'Run again', icon: RotateCcw, disabled: !i.request, onSelect: () => i.request && launch({ ...i.request }) }])
                  ]}
                />
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</Panel>

<InstanceDrawer bind:id={sel.id} />
