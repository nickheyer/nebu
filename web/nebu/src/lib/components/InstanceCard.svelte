<script lang="ts">
  import { goto } from '$app/navigation';
  import { live, clock, groupLabel, runtimeName } from '$lib/state.svelte';
  import { instanceMemory } from '$lib/instances';
  import { stopInstance } from '$lib/actions.svelte';
  import { count, duration, enumLabel, tone } from '$lib/format';
  import { InstanceState, type Instance } from '$proto/instance_pb';
  import { RouteState } from '$proto/gateway_pb';
  import { MessageSquare, Square, PanelRight, Terminal } from '@lucide/svelte';
  import Button from './ui/Button.svelte';
  import IconButton from './ui/IconButton.svelte';
  import Menu from './ui/Menu.svelte';
  import State from './ui/State.svelte';

  // A running instance outside any slot, laid out like a slot so the list reads as one
  let { instance }: { instance: Instance } = $props();

  const route = $derived(live.routes.get(instance.name));
  const answering = $derived(route?.state === RouteState.READY);
  const memory = $derived(instanceMemory(instance));
  const install = $derived(instance.installId ? live.installs.get(instance.installId) : undefined);
  const rails: Record<string, string> = { ok: 'bg-ok', warn: 'bg-warn', bad: 'bg-bad', neutral: 'bg-line-strong' };
  const rail = $derived(rails[tone(enumLabel(InstanceState, instance.state))] ?? rails.neutral);
  const href = $derived(`/instances/${instance.id}`);
  let stopping = $state(false);

  async function stop() {
    stopping = true;
    await stopInstance(instance.id, instance.name);
    stopping = false;
  }
</script>

<article class="card card-link relative grid grid-cols-[2.25rem_minmax(0,1fr)_auto] items-center gap-x-4 py-3 pr-3 pl-3">
  <span class="absolute inset-y-3 left-0 w-0.5 rounded-full {rail}"></span>
  <a {href} class="flex h-9 w-9 items-center justify-center rounded-md bg-raised text-fg-faint" aria-label="Open {instance.name}"><Terminal size={15} /></a>
  <a {href} class="grid min-w-0 grid-cols-1 gap-x-6 gap-y-1 sm:grid-cols-[minmax(8rem,14rem)_minmax(0,1fr)]">
    <div class="min-w-0">
      <div class="truncate font-mono text-sm font-semibold text-fg">{instance.name}</div>
      <div class="mt-1 flex items-center gap-2">
        <State values={InstanceState} value={instance.state} />
        <span class="text-xs tabular-nums text-fg-faint">{duration(instance.readyAt ?? instance.createdAt, undefined, clock.now)}</span>
      </div>
    </div>
    <div class="min-w-0">
      <div class="truncate text-sm text-fg" title={instance.repo}>{instance.repo}</div>
      <div class="mt-1 flex flex-wrap gap-x-3 truncate text-xs text-fg-muted"><span class="font-mono">{groupLabel(instance)}</span><span>{runtimeName(instance.runtimeId)}{install?.version ? ` ${install.version}` : ''}</span><span>No slot</span></div>
    </div>
  </a>
  <div class="flex items-center gap-4">
    <dl class="hidden items-center gap-5 text-right xl:flex">
      <div class="stat"><dt>Requests</dt><dd>{count(route?.requests ?? 0n)}{#if route?.inFlight}<span class="text-fg-faint"> · {route.inFlight} live</span>{/if}</dd></div>
      <div class="stat"><dt>Memory</dt><dd>{memory || '–'}</dd></div>
    </dl>
    <div class="flex items-center gap-1">
      {#if answering}<Button size="sm" variant="subtle" icon={MessageSquare} href="/chat?model={encodeURIComponent(instance.name)}">Chat</Button>{/if}
      <IconButton size="sm" icon={Square} label="Stop" variant="ghost" class="text-bad hover:text-bad" loading={stopping} onclick={stop} />
      <Menu size="sm" items={[{ label: 'Open', icon: PanelRight, onSelect: () => goto(href) }]} />
    </div>
  </div>
</article>
