<script lang="ts">
  import '../app.css';
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { connect, disconnect, live, activeTasks } from '$lib/state.svelte';
  import { FindingKind } from '$proto/monitor_pb';

  let { children } = $props();
  const links = [
    ['/', 'dashboard'],
    ['/catalog', 'catalog'],
    ['/store', 'store'],
    ['/runtimes', 'runtimes'],
    ['/slots', 'slots'],
    ['/instances', 'instances'],
    ['/tasks', 'tasks'],
    ['/monitor', 'monitor'],
    ['/host', 'host'],
    ['/settings', 'settings']
  ];
  const unacked = $derived([...live.findings.values()].filter((f) => !f.acknowledged && f.kind !== FindingKind.UNSPECIFIED).length);
  const running = $derived(activeTasks().length);

  onMount(() => {
    connect();
    return () => disconnect();
  });
</script>

<div class="flex h-full">
  <nav class="flex w-44 shrink-0 flex-col border-r border-zinc-800 bg-zinc-900/60 p-3">
    <a href="/" class="mb-4 px-2 text-lg font-bold tracking-tight">nebu</a>
    {#each links as [href, label] (href)}
      <a
        {href}
        class="rounded px-2 py-1.5 text-sm {page.url.pathname === href || (href !== '/' && page.url.pathname.startsWith(href)) ? 'bg-zinc-800 text-white' : 'text-zinc-400 hover:text-white'}"
      >
        {label}
        {#if label === 'tasks' && running}<span class="ml-1 rounded bg-amber-800 px-1 text-[10px]">{running}</span>{/if}
        {#if label === 'monitor' && unacked}<span class="ml-1 rounded bg-emerald-800 px-1 text-[10px]">{unacked}</span>{/if}
      </a>
    {/each}
    <div class="mt-auto px-2 text-xs {live.connected ? 'text-emerald-400' : 'text-red-400'}">
      {live.connected ? 'live' : 'disconnected'}
      {#if live.error}<div class="text-zinc-500" title={live.error}>{live.error.slice(0, 60)}</div>{/if}
    </div>
  </nav>
  <main class="flex-1 overflow-auto p-6">
    {@render children()}
  </main>
</div>
