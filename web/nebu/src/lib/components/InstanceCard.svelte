<script lang="ts">
  import { api } from '$lib/api';
  import { live, clock } from '$lib/state.svelte';
  import { instanceMemory } from '$lib/instances';
  import { count, duration } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { InstanceState, type Instance } from '$proto/instance_pb';
  import { RouteState } from '$proto/gateway_pb';
  import { MessageSquare, Square, PanelRight } from '@lucide/svelte';
  import Button from './ui/Button.svelte';
  import Menu from './ui/Menu.svelte';
  import StatePill from './ui/StatePill.svelte';

  // A running instance outside any slot, shown like a slot so the page reads as one grid
  let { instance, onOpen }: { instance: Instance; onOpen: (instance: Instance) => void } = $props();

  const route = $derived(live.routes.get(instance.name));
  const answering = $derived(route?.state === RouteState.READY);
  const memory = $derived(instanceMemory(instance));
  let stopping = $state(false);

  async function stop() {
    const yes = await confirm({ title: `Stop ${instance.name}?`, message: 'It will not relaunch after a daemon restart.', action: 'Stop', tone: 'bad' });
    if (!yes) return;
    stopping = true;
    try {
      await api.instances.stopInstance({ id: instance.id });
      ok(`Stopping ${instance.name}`);
    } catch (err) {
      fail(err, 'Stop failed');
    } finally {
      stopping = false;
    }
  }
</script>

<article class="card flex flex-col gap-4 p-5 transition-colors hover:border-line-strong">
  <div class="flex items-start gap-3">
    <button type="button" class="min-w-0 flex-1 text-left" onclick={() => onOpen(instance)}>
      <div class="truncate font-mono text-base font-semibold text-fg hover:text-accent">{instance.name}</div>
      <div class="text-sm text-fg-faint">Standalone</div>
    </button>
    <StatePill values={InstanceState} value={instance.state} />
  </div>

  <div class="min-h-[2.75rem]">
    <div class="truncate text-sm font-medium text-fg" title={instance.repo}>{instance.repo}</div>
    <div class="truncate text-sm text-fg-muted"><span class="font-mono">{instance.group}</span> on {instance.runtimeId}</div>
  </div>

  <dl class="grid grid-cols-3 gap-3 rounded-lg bg-sunken/60 px-3 py-2.5">
    <div><dt class="text-xs text-fg-faint">Up</dt><dd class="text-sm tabular-nums text-fg">{duration(instance.readyAt ?? instance.createdAt, undefined, clock.now)}</dd></div>
    <div><dt class="text-xs text-fg-faint">Requests</dt><dd class="text-sm tabular-nums text-fg">{count(route?.requests ?? 0n)}</dd></div>
    <div><dt class="text-xs text-fg-faint">Memory</dt><dd class="text-sm tabular-nums text-fg">{memory || '–'}</dd></div>
  </dl>

  <div class="mt-auto flex items-center gap-2 border-t border-line pt-4">
    {#if answering}<Button size="sm" icon={MessageSquare} href="/chat?model={encodeURIComponent(instance.name)}">Chat</Button>{/if}
    <Button size="sm" variant="danger" icon={Square} loading={stopping} onclick={stop}>Stop</Button>
    <span class="ml-auto"><Menu size="sm" items={[{ label: 'Details', icon: PanelRight, onSelect: () => onOpen(instance) }]} /></span>
  </div>
</article>
