<script lang="ts" module>
  export interface Choice {
    id: string;
    label: string;
    detail?: string;
    mono?: boolean;
    warn?: boolean;
    disabled?: boolean;
  }
</script>

<script lang="ts">
  // One of a few options as a list of rows, the chosen one marked
  let { items, value = $bindable(''), label, class: cls = '' }: { items: Choice[]; value?: string; label: string; class?: string } = $props();
</script>

<div role="radiogroup" aria-label={label} class="divide-y divide-line overflow-hidden rounded-md border border-line bg-sunken/40 {cls}">
  {#each items as c (c.id)}
    {@const on = value === c.id}
    <button
      type="button"
      role="radio"
      aria-checked={on}
      disabled={c.disabled}
      class="relative flex min-h-10 w-full items-center gap-3 px-3 py-2 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-40 {on ? 'bg-accent/8' : 'hover:bg-raised/50'}"
      onclick={() => (value = c.id)}
    >
      {#if on}<span class="absolute inset-y-0 left-0 w-0.5 bg-accent"></span>{/if}
      <span class="flex h-4 w-4 shrink-0 items-center justify-center rounded-full border {on ? 'border-accent' : 'border-line-strong'}">
        {#if on}<span class="h-2 w-2 rounded-full bg-accent"></span>{/if}
      </span>
      <span class="min-w-0 flex-1 truncate text-sm {c.mono ? 'font-mono' : ''} {on ? 'text-fg' : 'text-fg-muted'}">{c.label}</span>
      {#if c.detail}<span class="shrink-0 truncate text-xs {c.warn ? 'text-warn' : 'text-fg-faint'}">{c.detail}</span>{/if}
    </button>
  {/each}
</div>
