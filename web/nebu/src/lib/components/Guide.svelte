<script lang="ts">
  import { live, liveInstances, updateSettings } from '$lib/state.svelte';
  import { Check, ChevronRight, X } from '@lucide/svelte';
  import { plural } from '$lib/format';
  import IconButton from './ui/IconButton.svelte';

  // The three steps between a fresh host and a model answering, each marked off as the host gets there
  const installs = $derived(live.installs.size);
  const models = $derived(live.models.size);
  const running = $derived(liveInstances().length);
  const complete = $derived(installs > 0 && models > 0 && running > 0);
  const shown = $derived(live.ready && !!live.settings && !live.settings.setupDismissed && !complete);

  const steps = $derived([
    { label: 'Install a runtime', value: installs ? plural(installs, 'install') : '', done: installs > 0, href: '/runtimes' },
    { label: 'Pull a model', value: models ? plural(models, 'model') : '', done: models > 0, href: '/catalog' },
    { label: 'Run it', value: running ? plural(running, 'running', 'running') : '', done: running > 0, href: models ? '/store' : '/catalog' }
  ]);
  const next = $derived(steps.findIndex((s) => !s.done));
</script>

{#if shown}
  <nav class="flex items-center gap-1 rounded-md bg-surface py-1.5 pr-1.5 pl-2" aria-label="Setup">
    {#each steps as s, i (s.label)}
      {@const current = i === next}
      <a href={s.href} class="flex h-8 items-center gap-2 rounded-md px-2 text-sm transition-colors hover:bg-raised {current ? 'text-fg' : s.done ? 'text-fg-faint' : 'text-fg-muted'}">
        <span class="flex h-5 w-5 shrink-0 items-center justify-center rounded-full text-[11px] font-semibold tabular-nums {s.done ? 'bg-ok/15 text-ok' : current ? 'bg-accent text-accent-fg' : 'bg-raised text-fg-muted'}">
          {#if s.done}<Check size={12} strokeWidth={3} />{:else}{i + 1}{/if}
        </span>
        <span class={s.done ? 'line-through decoration-fg-faint/60' : ''}>{s.label}</span>
        {#if s.value}<span class="text-xs text-fg-faint">{s.value}</span>{/if}
      </a>
      {#if i < steps.length - 1}<ChevronRight size={14} class="shrink-0 text-fg-faint/60" />{/if}
    {/each}
    <span class="ml-auto"><IconButton size="sm" icon={X} label="Dismiss" onclick={() => void updateSettings({ setupDismissed: true })} /></span>
  </nav>
{/if}
