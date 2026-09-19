<script lang="ts">
  import '../app.css';
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { Tooltip } from 'bits-ui';
  import { connect, disconnect, live, activeTasks, hostName, hostLabeled } from '$lib/state.svelte';
  import { LayoutGrid, Boxes, MessageSquare, ListChecks, Cpu, Settings, WifiOff, KeyRound, Menu as MenuIcon, X, Activity, Bot } from '@lucide/svelte';
  import { sweepStale } from '$lib/images';
  import Logo from '$lib/components/Logo.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';
  import Toaster from '$lib/components/ui/Toaster.svelte';
  import Confirmer from '$lib/components/ui/Confirmer.svelte';
  import RunDialog from '$lib/components/RunDialog.svelte';

  let { children } = $props();
  let menuOpen = $state(false);

  interface Item {
    href: string;
    label: string;
    icon: typeof LayoutGrid;
    also?: string[];
    count?: number;
    busy?: boolean;
  }

  const groups = $derived<Item[][]>([
    [
      { href: '/', label: 'Serve', icon: LayoutGrid, also: ['/slots', '/instances'] },
      { href: '/store', label: 'Models', icon: Boxes, also: ['/catalog'] },
      { href: '/chat', label: 'Chat', icon: MessageSquare, also: ['/generate'] },
      { href: '/bots', label: 'Bots', icon: Bot }
    ],
    [
      { href: '/requests', label: 'Requests', icon: Activity },
      { href: '/tasks', label: 'Tasks', icon: ListChecks, count: activeTasks().length || undefined, busy: activeTasks().length > 0 },
      { href: '/runtimes', label: 'Runtimes', icon: Cpu }
    ]
  ]);

  const name = $derived(hostName());
  const onHost = $derived(page.url.pathname === '/host');
  const wide = $derived(page.url.pathname === '/chat');

  function active(item: { href: string; also?: string[] }): boolean {
    const p = page.url.pathname;
    if (item.href === '/') return p === '/' || (item.also ?? []).some((h) => p.startsWith(h + '/'));
    return [item.href, ...(item.also ?? [])].some((h) => p === h || p.startsWith(h + '/'));
  }

  onMount(() => {
    connect();
    // Remove unreferenced files once per page load.
    sweepStale().catch(() => {});
    return () => disconnect();
  });
</script>

<svelte:head>
  <title>{name ? `${name} · nebu` : 'nebu'}</title>
</svelte:head>

{#snippet navItem(item: Item)}
  {@const Icon = item.icon}
  {@const on = active(item)}
  <a
    href={item.href}
    class="relative flex h-8 items-center gap-2.5 rounded-md px-2.5 text-sm transition-colors {on ? 'bg-raised/70 font-medium text-fg' : 'text-fg-muted hover:bg-raised/50 hover:text-fg'}"
    aria-current={on ? 'page' : undefined}
    onclick={() => (menuOpen = false)}
  >
    {#if on}<span class="absolute inset-y-1.5 left-0 w-0.5 rounded-full bg-accent"></span>{/if}
    {#if item.busy}
      <Spinner size={15} class="text-accent" />
    {:else}
      <Icon size={15} class={on ? 'text-accent' : 'text-fg-faint'} />
    {/if}
    <span class="flex-1">{item.label}</span>
    {#if item.count}
      <span class="min-w-5 rounded-full bg-raised px-1.5 text-center text-[11px] font-semibold tabular-nums text-fg-muted">{item.count}</span>
    {/if}
  </a>
{/snippet}

<Tooltip.Provider delayDuration={250}>
  <div class="flex min-h-screen">
    {#if menuOpen}
      <button class="fade fixed inset-0 z-30 bg-black/50 lg:hidden" aria-label="Close menu" onclick={() => (menuOpen = false)}></button>
    {/if}
    <nav
      class="fixed inset-y-0 left-0 z-40 flex h-screen w-56 shrink-0 flex-col border-r border-line bg-bg transition-transform lg:translate-x-0 {menuOpen ? 'translate-x-0 shadow-pop' : '-translate-x-full'}"
      aria-label="Main"
    >
      <div class="flex h-14 items-center gap-3 px-4">
        <a href="/" class="flex items-center gap-2.5 text-fg" aria-label="nebu">
          <Logo size={24} />
          <span class="font-mono text-[13px] font-semibold tracking-[0.3em] text-fg">NEBU</span>
        </a>
        <button class="ml-auto rounded-md p-1.5 text-fg-faint hover:bg-raised hover:text-fg lg:hidden" aria-label="Close menu" onclick={() => (menuOpen = false)}><X size={15} /></button>
      </div>

      <a href="/host" class="relative mx-2 mb-3 flex flex-col rounded-md px-2.5 py-2 transition-colors {onHost ? 'bg-raised/70' : 'hover:bg-raised/50'}" aria-current={onHost ? 'page' : undefined} onclick={() => (menuOpen = false)}>
        {#if onHost}<span class="absolute inset-y-2 left-0 w-0.5 rounded-full bg-accent"></span>{/if}
        <span class="flex items-center gap-2">
          <span class="dot {live.connected ? 'text-ok pulse' : 'text-bad'}"></span>
          <span class="truncate text-sm font-medium text-fg" title={name}>{name || 'Connecting…'}</span>
        </span>
        <span class="mt-0.5 truncate pl-3.5 text-xs text-fg-faint" title={live.host?.hostname}>
          {#if !live.connected}Reconnecting{:else if hostLabeled()}{live.host?.hostname}{:else if live.host}{live.host.os}/{live.host.arch}{/if}
        </span>
      </a>

      <div class="flex-1 overflow-y-auto px-2">
        {#each groups as g, i (i)}
          <div class="flex flex-col gap-0.5 {i > 0 ? 'mt-4 border-t border-line pt-4' : ''}">
            {#each g as item (item.href)}{@render navItem(item)}{/each}
          </div>
        {/each}
      </div>

      <div class="border-t border-line px-2 py-2">
        {@render navItem({ href: '/settings', label: 'Settings', icon: Settings })}
      </div>
    </nav>

    <div class="flex min-w-0 flex-1 flex-col overflow-x-clip lg:pl-56">
      <div class="flex h-14 items-center gap-3 border-b border-line bg-bg px-4 lg:hidden">
        <button class="rounded-md p-1.5 text-fg-muted hover:bg-raised hover:text-fg" aria-label="Open menu" onclick={() => (menuOpen = true)}><MenuIcon size={17} /></button>
        <Logo size={22} class="text-fg" />
        <span class="text-sm font-semibold text-fg">{name || 'nebu'}</span>
        <span class="dot ml-auto {live.connected ? 'text-ok' : 'text-bad'}"></span>
      </div>
      {#if !live.connected && live.error}
        <div class="flex items-center gap-3 border-b px-8 py-2 text-sm {live.needsToken ? 'border-warn/25 bg-warn/8 text-warn' : 'border-bad/25 bg-bad/8 text-bad'}">
          {#if live.needsToken}
            <KeyRound size={15} />
            <span>API token required</span>
            <a href="/settings" class="font-medium underline underline-offset-2">Set it in Settings</a>
          {:else}
            <WifiOff size={15} />
            <span>Daemon unreachable</span>
            <span class="truncate text-xs opacity-80">{live.error}</span>
          {/if}
        </div>
      {/if}
      <main class="w-full flex-1 px-5 py-6 sm:px-6 {wide ? 'lg:px-6 lg:py-5' : 'lg:px-8 lg:py-7'}">
        {@render children()}
      </main>
    </div>
  </div>
  <RunDialog />
  <Confirmer />
  <Toaster />
</Tooltip.Provider>
