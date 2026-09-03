<script lang="ts">
  import { toasts, dismiss } from '$lib/toast.svelte';
  import { CircleCheck, CircleAlert, Info, TriangleAlert, X } from '@lucide/svelte';

  const icons = { ok: CircleCheck, bad: CircleAlert, warn: TriangleAlert, info: Info, accent: Info, neutral: Info };
  const colors = { ok: 'text-ok', bad: 'text-bad', warn: 'text-warn', info: 'text-info', accent: 'text-accent', neutral: 'text-fg-muted' };
</script>

<div class="pointer-events-none fixed right-4 bottom-4 z-[80] flex w-[min(24rem,calc(100vw-2rem))] flex-col gap-2">
  {#each toasts as t (t.id)}
    {@const Icon = icons[t.tone]}
    <div class="enter-up pointer-events-auto flex items-start gap-3 rounded-lg border border-line bg-overlay px-3.5 py-3 shadow-pop">
      <Icon size={16} class="mt-0.5 shrink-0 {colors[t.tone]}" />
      <div class="min-w-0 flex-1">
        <div class="text-sm font-medium text-fg">{t.title}</div>
        {#if t.detail}<div class="mt-0.5 text-xs leading-5 break-words text-fg-muted">{t.detail}</div>{/if}
        {#if t.href}<a href={t.href} class="link mt-1 inline-block text-xs" onclick={() => dismiss(t.id)}>{t.linkLabel ?? 'View'}</a>{/if}
      </div>
      <button class="-mr-1 rounded p-0.5 text-fg-faint hover:text-fg" onclick={() => dismiss(t.id)} aria-label="Dismiss"><X size={14} /></button>
    </div>
  {/each}
</div>
