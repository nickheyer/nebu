<script lang="ts">
  import '../app.css';
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { Tooltip } from 'bits-ui';
  import { connect, disconnect, live, unackedFindings, liveInstances } from '$lib/state.svelte';
  import { endDrag } from '$lib/dnd.svelte';
  import { LayoutDashboard, Search, Database, LayoutGrid, Boxes, Waypoints, Wrench, ListChecks, Radar, Server, Settings, WifiOff, KeyRound } from '@lucide/svelte';
  import Toaster from '$lib/components/ui/Toaster.svelte';
  import Confirmer from '$lib/components/ui/Confirmer.svelte';
  import ActivityMenu from '$lib/components/ActivityMenu.svelte';
  import SlotDialogs from '$lib/components/SlotDialogs.svelte';

  let { children } = $props();

  const groups = $derived([
    { label: '', items: [{ href: '/', label: 'Overview', icon: LayoutDashboard }] },
    {
      label: 'Models',
      items: [
        { href: '/catalog', label: 'Catalog', icon: Search },
        { href: '/store', label: 'Store', icon: Database }
      ]
    },
    {
      label: 'Serving',
      items: [
        { href: '/slots', label: 'Slots', icon: LayoutGrid, count: live.slots.size || undefined },
        { href: '/instances', label: 'Instances', icon: Boxes, count: liveInstances().length || undefined },
        { href: '/gateway', label: 'Gateway', icon: Waypoints }
      ]
    },
    {
      label: 'System',
      items: [
        { href: '/runtimes', label: 'Runtimes', icon: Wrench },
        { href: '/tasks', label: 'Tasks', icon: ListChecks },
        { href: '/monitor', label: 'Monitor', icon: Radar, count: unackedFindings().length || undefined, tone: 'accent' },
        { href: '/host', label: 'Host', icon: Server }
      ]
    }
  ]);

  function active(href: string): boolean {
    const p = page.url.pathname;
    return href === '/' ? p === '/' : p === href || p.startsWith(href + '/');
  }

  onMount(() => {
    connect();
    const stop = () => endDrag();
    window.addEventListener('dragend', stop);
    window.addEventListener('drop', stop);
    return () => {
      disconnect();
      window.removeEventListener('dragend', stop);
      window.removeEventListener('drop', stop);
    };
  });
</script>

<Tooltip.Provider delayDuration={250}>
  <div class="flex h-full min-h-screen">
    <nav class="sticky top-0 flex h-screen w-[232px] shrink-0 flex-col border-r border-line bg-surface/70">
      <a href="/" class="flex h-14 items-center gap-2.5 border-b border-line px-4">
        <span class="flex h-7 w-7 items-center justify-center rounded-md bg-accent text-accent-fg">
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 18V6l16 12V6" /></svg>
        </span>
        <span class="text-base font-semibold tracking-tight text-fg">nebu</span>
        {#if live.host?.hostname}<span class="ml-auto truncate font-mono text-[11px] text-fg-faint" title={live.host.hostname}>{live.host.hostname}</span>{/if}
      </a>

      <div class="flex-1 overflow-y-auto px-2.5 py-3">
        {#each groups as g (g.label)}
          {#if g.label}<div class="mt-4 mb-1 px-2.5 text-[10.5px] font-semibold tracking-wider text-fg-faint uppercase">{g.label}</div>{/if}
          {#each g.items as item (item.href)}
            {@const Icon = item.icon}
            {@const on = active(item.href)}
            <a
              href={item.href}
              class="mb-0.5 flex items-center gap-2.5 rounded-md px-2.5 py-1.5 text-sm transition-colors {on ? 'bg-raised text-fg' : 'text-fg-muted hover:bg-raised/60 hover:text-fg'}"
              aria-current={on ? 'page' : undefined}
            >
              <Icon size={15} class={on ? 'text-accent' : ''} />
              <span class="flex-1">{item.label}</span>
              {#if item.count}
                <span class="rounded-full px-1.5 text-[10.5px] font-semibold tabular-nums {'tone' in item && item.tone === 'accent' ? 'bg-accent text-accent-fg' : 'bg-line text-fg-muted'}">{item.count}</span>
              {/if}
            </a>
          {/each}
        {/each}
      </div>

      <div class="border-t border-line px-2.5 py-2.5">
        <ActivityMenu />
        <a href="/settings" class="flex items-center gap-2.5 rounded-md px-2.5 py-2 text-sm text-fg-muted transition-colors hover:bg-raised hover:text-fg {active('/settings') ? 'bg-raised text-fg' : ''}">
          <Settings size={15} />
          <span class="flex-1">Settings</span>
        </a>
        <div class="mt-1 flex items-center gap-2 px-2.5 py-1.5 text-[11.5px]" title={live.error}>
          <span class="relative inline-block h-1.5 w-1.5 rounded-full {live.connected ? 'bg-ok' : 'bg-bad'} {live.connected ? 'pulse' : ''}"></span>
          <span class="text-fg-faint">{live.connected ? 'Live updates' : 'Reconnecting'}</span>
        </div>
      </div>
    </nav>

    <div class="flex min-w-0 flex-1 flex-col">
      {#if !live.connected && live.error}
        <div class="flex items-center gap-3 border-b px-6 py-2 text-sm {live.needsToken ? 'border-warn/30 bg-warn/10 text-warn' : 'border-bad/30 bg-bad/10 text-bad'}">
          {#if live.needsToken}
            <KeyRound size={15} />
            <span>The daemon requires an API token.</span>
            <a href="/settings" class="font-medium underline underline-offset-2">Enter it in settings</a>
          {:else}
            <WifiOff size={15} />
            <span>Not connected to the daemon, retrying.</span>
            <span class="truncate text-xs opacity-80">{live.error}</span>
          {/if}
        </div>
      {/if}
      <main class="mx-auto w-full max-w-[1480px] flex-1 px-8 py-7">
        {@render children()}
      </main>
    </div>
  </div>
  <SlotDialogs />
  <Confirmer />
  <Toaster />
</Tooltip.Provider>
