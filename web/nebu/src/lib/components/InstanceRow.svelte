<script lang="ts">
  import { live, clock } from '$lib/state.svelte';
  import { instanceMemory } from '$lib/instances';
  import { stopInstance } from '$lib/slotActions.svelte';
  import { count, duration, enumLabel, tone } from '$lib/format';
  import { InstanceState, type Instance } from '$proto/instance_pb';
  import { RouteState } from '$proto/gateway_pb';
  import { MessageSquare, Square, PanelRight } from '@lucide/svelte';
  import Button from './ui/Button.svelte';
  import IconButton from './ui/IconButton.svelte';
  import Menu from './ui/Menu.svelte';
  import State from './ui/State.svelte';

  // A running instance outside any slot, laid out like a slot so the list reads as one
  let { instance, onOpen }: { instance: Instance; onOpen: (instance: Instance) => void } = $props();

  const route = $derived(live.routes.get(instance.name));
  const answering = $derived(route?.state === RouteState.READY);
  const memory = $derived(instanceMemory(instance));
  const install = $derived(instance.installId ? live.installs.get(instance.installId) : undefined);
  const rails: Record<string, string> = { ok: 'bg-ok', warn: 'bg-warn', bad: 'bg-bad', neutral: 'bg-line-strong' };
  const rail = $derived(rails[tone(enumLabel(InstanceState, instance.state))] ?? rails.neutral);
  let stopping = $state(false);

  async function stop() {
    stopping = true;
    await stopInstance(instance.id, instance.name);
    stopping = false;
  }
</script>

<article class="bay grid grid-cols-[minmax(8rem,12rem)_minmax(0,1fr)_auto] items-center gap-x-6 py-3 pr-2 pl-4">
  <span class="absolute inset-y-2.5 left-0 w-0.5 rounded-full {rail}"></span>
  <button type="button" class="min-w-0 text-left" onclick={() => onOpen(instance)}>
    <div class="truncate font-mono text-sm font-semibold text-fg">{instance.name}</div>
    <div class="mt-1 flex items-center gap-2">
      <State values={InstanceState} value={instance.state} />
      <span class="text-xs text-fg-faint">standalone</span>
      <span class="text-xs tabular-nums text-fg-faint">{duration(instance.readyAt ?? instance.createdAt, undefined, clock.now)}</span>
    </div>
  </button>
  <button type="button" class="min-w-0 text-left" onclick={() => onOpen(instance)}>
    <div class="truncate text-sm text-fg" title={instance.repo}>{instance.repo}</div>
    <div class="mt-1 truncate text-xs text-fg-muted"><span class="font-mono">{instance.group}</span> · {instance.runtimeId}{install?.version ? ` ${install.version}` : ''}</div>
  </button>
  <div class="flex items-center gap-5">
    <dl class="hidden items-center gap-5 text-right lg:flex">
      <div><dt class="caps text-fg-faint">Requests</dt><dd class="text-sm tabular-nums text-fg">{count(route?.requests ?? 0n)}</dd></div>
      <div><dt class="caps text-fg-faint">Memory</dt><dd class="text-sm tabular-nums text-fg">{memory || '–'}</dd></div>
    </dl>
    <div class="flex items-center gap-1">
      {#if answering}<Button size="sm" variant="subtle" icon={MessageSquare} href="/chat?model={encodeURIComponent(instance.name)}">Chat</Button>{/if}
      <IconButton size="sm" icon={Square} label="Stop" variant="ghost" class="text-bad hover:text-bad" loading={stopping} onclick={stop} />
      <Menu size="sm" items={[{ label: 'Details', icon: PanelRight, onSelect: () => onOpen(instance) }]} />
    </div>
  </div>
</article>
