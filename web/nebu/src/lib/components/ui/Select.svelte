<script lang="ts" module>
  export interface SelectItem {
    value: string;
    label: string;
    // A short line beside the label, muted
    detail?: string;
    disabled?: boolean;
    group?: string;
  }
</script>

<script lang="ts">
  import { Select } from 'bits-ui';
  import { Check, ChevronDown } from '@lucide/svelte';

  // One select for every choice, an empty value allowed as a real option
  let {
    value = $bindable(''),
    items,
    placeholder = 'Choose',
    id,
    mono = false,
    disabled = false,
    size = 'md',
    label,
    class: cls = ''
  }: {
    value?: string;
    items: SelectItem[];
    placeholder?: string;
    id?: string;
    mono?: boolean;
    disabled?: boolean;
    size?: 'sm' | 'md';
    // Read by screen readers when no visible label points at the select
    label?: string;
    class?: string;
  } = $props();

  // The library treats an empty value as nothing chosen, so empty travels under a sentinel
  const NONE = '__none__';
  const wrap = (v: string) => (v === '' ? NONE : v);
  const unwrap = (v: string) => (v === NONE ? '' : v);

  const current = $derived(items.find((i) => i.value === value));
  const groups = $derived.by(() => {
    const out: { group: string; items: SelectItem[] }[] = [];
    for (const i of items) {
      const g = i.group ?? '';
      let bucket = out.find((b) => b.group === g);
      if (!bucket) out.push((bucket = { group: g, items: [] }));
      bucket.items.push(i);
    }
    return out;
  });
  const height = $derived(size === 'sm' ? 'h-7 text-xs' : 'h-8 text-sm');
</script>

<div class="min-w-0 {cls || 'w-full'}">
<Select.Root type="single" value={wrap(value)} onValueChange={(v) => (value = unwrap(v))} {disabled} items={items.map((i) => ({ value: wrap(i.value), label: i.label, disabled: i.disabled }))}>
  <Select.Trigger
    {id}
    aria-label={label}
    class="inline-flex w-full items-center gap-2 rounded-md border border-line bg-sunken px-2.5 text-left text-fg transition-colors hover:border-line-strong focus:border-accent focus:outline-none disabled:cursor-not-allowed disabled:opacity-50 data-[state=open]:border-accent {height}"
  >
    <span class="min-w-0 flex-1 truncate {mono ? 'font-mono' : ''} {current ? '' : 'text-fg-faint'}">
      {current?.label ?? placeholder}{#if current?.detail}<span class="ml-1.5 text-fg-faint">{current.detail}</span>{/if}
    </span>
    <ChevronDown size={14} class="shrink-0 text-fg-faint" />
  </Select.Trigger>
  <Select.Portal>
    <Select.Content sideOffset={4} class="enter-up z-[70] max-h-80 min-w-[var(--bits-select-anchor-width)] overflow-hidden rounded-md border border-line bg-overlay shadow-pop focus:outline-none">
      <Select.Viewport class="max-h-80 overflow-y-auto p-1">
        {#each groups as g (g.group)}
          {#if g.group}
            <Select.Group>
              <Select.GroupHeading class="caps px-2 pt-2 pb-1 text-fg-faint">{g.group}</Select.GroupHeading>
              {#each g.items as i (i.value)}
                {@render item(i)}
              {/each}
            </Select.Group>
          {:else}
            {#each g.items as i (i.value)}
              {@render item(i)}
            {/each}
          {/if}
        {/each}
      </Select.Viewport>
    </Select.Content>
  </Select.Portal>
</Select.Root>
</div>

{#snippet item(i: SelectItem)}
  <Select.Item value={wrap(i.value)} label={i.label} disabled={i.disabled} class="flex cursor-pointer items-center gap-2 rounded-[5px] px-2 py-1.5 text-sm outline-none select-none data-[disabled]:cursor-not-allowed data-[disabled]:opacity-40 data-[highlighted]:bg-raised">
    {#snippet children({ selected })}
      <span class="min-w-0 flex-1 truncate {mono ? 'font-mono' : ''} {selected ? 'text-fg' : 'text-fg-muted'}">
        {i.label}{#if i.detail}<span class="ml-1.5 text-fg-faint">{i.detail}</span>{/if}
      </span>
      {#if selected}<Check size={13} class="shrink-0 text-accent" />{/if}
    {/snippet}
  </Select.Item>
{/snippet}
