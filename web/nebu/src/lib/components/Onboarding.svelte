<script lang="ts">
  import { live, liveInstances } from '$lib/state.svelte';
  import { Check, ChevronRight } from '@lucide/svelte';

  const steps = $derived([
    { done: live.installs.size > 0, title: 'Install a runtime', detail: 'Adopt a binary already on the host, download a prebuilt release, or build one from a recipe.', href: '/runtimes' },
    { done: live.models.size > 0, title: 'Pull a model', detail: 'Search a source, check which weight groups fit this host, and pull one into the store.', href: '/catalog' },
    { done: live.slots.size > 0, title: 'Create a slot', detail: 'Reserve devices and a memory budget under a public name your router points at once.', href: '/slots' },
    { done: liveInstances().length > 0, title: 'Serve it', detail: 'Drag a stored model onto a slot, or run it straight from the store.', href: '/store' }
  ]);
  const remaining = $derived(steps.filter((s) => !s.done).length);
</script>

{#if remaining > 0}
  <section class="panel overflow-hidden">
    <div class="flex items-center gap-3 border-b border-line px-4 py-3">
      <h2 class="text-sm font-semibold text-fg">Get serving</h2>
      <span class="text-xs text-fg-faint">{steps.length - remaining} of {steps.length} done</span>
    </div>
    <ol class="grid grid-cols-1 divide-y divide-line md:grid-cols-2 md:divide-x md:divide-y-0 xl:grid-cols-4">
      {#each steps as s, i (s.title)}
        <li>
          <a href={s.href} class="group flex h-full items-start gap-3 px-4 py-3.5 transition-colors hover:bg-raised/50 {s.done ? 'opacity-60' : ''}">
            <span class="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full border text-[11px] font-semibold {s.done ? 'border-ok bg-ok/15 text-ok' : 'border-line-strong text-fg-muted'}">
              {#if s.done}<Check size={11} strokeWidth={3} />{:else}{i + 1}{/if}
            </span>
            <span class="min-w-0 flex-1">
              <span class="flex items-center gap-1 text-sm font-medium text-fg">{s.title}<ChevronRight size={13} class="text-fg-faint opacity-0 transition-opacity group-hover:opacity-100" /></span>
              <span class="mt-0.5 block text-xs leading-5 text-fg-muted">{s.detail}</span>
            </span>
          </a>
        </li>
      {/each}
    </ol>
  </section>
{/if}
