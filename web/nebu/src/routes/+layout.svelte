<script lang="ts">
  import '../app.css';
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { Tooltip } from 'bits-ui';
  import { connect, disconnect, live, unackedFindings, activeTasks, hostName } from '$lib/state.svelte';
  import { LayoutGrid, Boxes, MessageSquare, ListChecks, Radar, Cpu, Server, Settings, WifiOff, KeyRound, Menu as MenuIcon, X } from '@lucide/svelte';
  import Logo from '$lib/components/Logo.svelte';
  import Spinner from '$lib/components/ui/Spinner.svelte';
  import Toaster from '$lib/components/ui/Toaster.svelte';
  import Confirmer from '$lib/components/ui/Confirmer.svelte';
  import SlotDialogs from '$lib/components/SlotDialogs.svelte';

  let { children } = $props();
  let menuOpen = $state(false);

  interface Item {
    href: string;
    label: string;
    icon: typeof LayoutGrid;
    // Other paths this item stands for
    also?: string[];
    count?: number;
    busy?: boolean;
    tone?: 'accent';
  }

  const groups = $derived<Item[][]>([
    [
      { href: '/', label: 'Serve', icon: LayoutGrid },
      { href: '/store', label: 'Models', icon: Boxes, also: ['/catalog'] },
      { href: '/chat', label: 'Chat', icon: MessageSquare }
    ],
    [
      { href: '/tasks', label: 'Tasks', icon: ListChecks, count: activeTasks().length || undefined, busy: activeTasks().length > 0 },
      { href: '/monitor', label: 'Monitor', icon: Radar, count: unackedFindings().length || undefined, tone: 'accent' }
    ],
    [
      { href: '/runtimes', label: 'Runtimes', icon: Cpu },
      { href: '/host', label: 'Host', icon: Server }
    ]
  ]);

  const name = $derived(hostName());
  const labeled = $derived(!!live.settings?.hostLabel && live.settings.hostLabel !== live.host?.hostname);

  function active(item: { href: string; also?: string[] }): boolean {
    const p = page.url.pathname;
    if (item.href === '/') return p === '/';
    return [item.href, ...(item.also ?? [])].some((h) => p === h || p.startsWith(h + '/'));
  }

  onMount(() => {
    connect();
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
    class="flex h-9 items-center gap-3 rounded-lg px-3 text-sm transition-colors {on ? 'bg-raised font-medium text-fg' : 'text-fg-muted hover:bg-raised/60 hover:text-fg'}"
    aria-current={on ? 'page' : undefined}
    onclick={() => (menuOpen = false)}
  >
    {#if item.busy}
      <Spinner size={16} class="text-accent" />
    {:else}
      <Icon size={16} class={on ? 'text-accent' : 'text-fg-faint'} />
    {/if}
    <span class="flex-1">{item.label}</span>
    {#if item.count}
      <span class="min-w-5 rounded-full px-1.5 text-center text-xs font-semibold tabular-nums {item.tone === 'accent' ? 'bg-accent/15 text-accent' : 'bg-raised text-fg-muted'}">{item.count}</span>
    {/if}
  </a>
{/snippet}

<Tooltip.Provider delayDuration={250}>
  <div class="flex min-h-screen">
    {#if menuOpen}
      <button class="fade fixed inset-0 z-30 bg-black/50 lg:hidden" aria-label="Close menu" onclick={() => (menuOpen = false)}></button>
    {/if}
    <nav
      class="fixed inset-y-0 left-0 z-40 flex h-screen w-60 shrink-0 flex-col border-r border-line bg-bg transition-transform lg:sticky lg:top-0 lg:translate-x-0 {menuOpen ? 'translate-x-0 shadow-pop' : '-translate-x-full'}"
      aria-label="Main"
    >
      <div class="flex h-14 items-center gap-3 px-5">
        <a href="/" class="flex items-center gap-2.5 text-fg">
          <Logo size={24} />
          <span class="text-lg font-semibold tracking-tight">nebu</span>
        </a>
        <button class="ml-auto rounded-lg p-1.5 text-fg-faint hover:bg-raised hover:text-fg lg:hidden" aria-label="Close menu" onclick={() => (menuOpen = false)}><X size={16} /></button>
      </div>

      <div class="mx-3 mb-2 rounded-lg border border-line bg-surface px-3 py-2.5">
        <div class="flex items-center gap-2">
          <span class="relative inline-block h-2 w-2 shrink-0 rounded-full {live.connected ? 'bg-ok' : 'bg-bad'} {live.connected ? 'pulse' : ''}"></span>
          <span class="truncate text-sm font-medium text-fg" title={name}>{name || 'Connecting'}</span>
        </div>
        <div class="mt-0.5 truncate pl-4 text-xs text-fg-faint" title={live.host?.hostname}>
          {#if !live.connected}Reconnecting{:else if labeled}{live.host?.hostname}{:else if live.host}{live.host.os}/{live.host.arch}{/if}
        </div>
      </div>

      <div class="flex-1 overflow-y-auto px-3 py-2">
        {#each groups as g, i (i)}
          <div class="flex flex-col gap-0.5 {i > 0 ? 'mt-4 border-t border-line pt-4' : ''}">
            {#each g as item (item.href)}{@render navItem(item)}{/each}
          </div>
        {/each}
      </div>

      <div class="border-t border-line px-3 py-3">
        {@render navItem({ href: '/settings', label: 'Settings', icon: Settings })}
      </div>
    </nav>

    <div class="flex min-w-0 flex-1 flex-col">
      <div class="flex h-14 items-center gap-3 border-b border-line bg-bg px-4 lg:hidden">
        <button class="rounded-lg p-1.5 text-fg-muted hover:bg-raised hover:text-fg" aria-label="Open menu" onclick={() => (menuOpen = true)}><MenuIcon size={18} /></button>
        <Logo size={20} class="text-fg" />
        <span class="text-sm font-semibold text-fg">{name || 'nebu'}</span>
        <span class="relative ml-auto inline-block h-2 w-2 rounded-full {live.connected ? 'bg-ok' : 'bg-bad'}"></span>
      </div>
      {#if !live.connected && live.error}
        <div class="flex items-center gap-3 border-b px-8 py-2.5 text-sm {live.needsToken ? 'border-warn/30 bg-warn/10 text-warn' : 'border-bad/30 bg-bad/10 text-bad'}">
          {#if live.needsToken}
            <KeyRound size={16} />
            <span>The daemon needs an API token.</span>
            <a href="/settings" class="font-medium underline underline-offset-2">Enter it in settings</a>
          {:else}
            <WifiOff size={16} />
            <span>Daemon unreachable, retrying.</span>
            <span class="truncate text-xs opacity-80">{live.error}</span>
          {/if}
        </div>
      {/if}
      <main class="mx-auto w-full max-w-[1320px] flex-1 px-5 py-6 sm:px-8 sm:py-8">
        {@render children()}
      </main>
    </div>
  </div>
  <SlotDialogs />
  <Confirmer />
  <Toaster />
</Tooltip.Provider>
