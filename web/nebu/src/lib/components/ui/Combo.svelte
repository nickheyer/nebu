<script lang="ts" module>
  export interface ComboItem {
    value: string;
    label: string;
    detail?: string;
    group?: string;
  }
</script>

<script lang="ts">
  import { Combobox } from 'bits-ui';
  import { Check, ChevronDown } from '@lucide/svelte';

  let {
    value = $bindable(''),
    items,
    id,
    mono = false,
    disabled = false,
    empty = '',
    invalid = false,
    onchange,
    class: cls = ''
  }: {
    value?: string;
    items: ComboItem[];
    id?: string;
    mono?: boolean;
    disabled?: boolean;
    // Placeholder shown while nothing is typed
    empty?: string;
    invalid?: boolean;
    onchange?: (value: string) => void;
    class?: string;
  } = $props();

  // Typed text narrows the list, a pick or an opened menu shows every item
  let query = $state('');
  const shown = $derived.by(() => {
    const q = query.trim().toLowerCase();
    const list = q ? items.filter((i) => i.value.toLowerCase().includes(q) || i.label.toLowerCase().includes(q) || (i.detail ?? '').toLowerCase().includes(q)) : items;
    const out: { group: string; items: ComboItem[] }[] = [];
    for (const i of list) {
      const g = i.group ?? '';
      let bucket = out.find((b) => b.group === g);
      if (!bucket) out.push((bucket = { group: g, items: [] }));
      bucket.items.push(i);
    }
    return out;
  });
  function set(next: string) {
    value = next;
    onchange?.(next);
  }
</script>

<div class="min-w-0 {cls || 'w-full'}">
  <Combobox.Root
    type="single"
    {value}
    inputValue={value}
    onValueChange={(v) => {
      if (v !== '') set(v);
    }}
    onOpenChange={(open) => {
      if (open) query = '';
    }}
    {disabled}
    items={items.map((i) => ({ value: i.value, label: i.value }))}
  >
    <div class="flex h-9 w-full items-center rounded-md border bg-sunken text-sm text-fg transition-colors focus-within:border-accent {invalid ? 'border-bad' : 'border-line hover:border-line-strong'} {disabled ? 'cursor-not-allowed opacity-50' : ''}">
      <Combobox.Input
        {id}
        class="input h-full! min-w-0 flex-1 border-0 bg-transparent px-2.5 focus-visible:outline-none {mono ? 'font-mono' : ''}"
        placeholder={empty}
        aria-invalid={invalid}
        autocomplete="off"
        spellcheck="false"
        oninput={(e) => {
          query = e.currentTarget.value;
          set(e.currentTarget.value);
        }}
      />
      <Combobox.Trigger class="flex h-full shrink-0 items-center px-2 text-fg-faint hover:text-fg" aria-label="Show suggestions" {disabled}>
        <ChevronDown size={14} />
      </Combobox.Trigger>
    </div>
    <Combobox.Portal>
      <Combobox.Content sideOffset={4} class="enter-up z-[70] max-h-80 min-w-[var(--bits-floating-anchor-width)] overflow-hidden rounded-md border border-line bg-overlay shadow-pop focus:outline-none">
        <Combobox.Viewport class="max-h-80 overflow-y-auto p-1">
          {#each shown as g (g.group)}
            {#if g.group}
              <Combobox.Group>
                <Combobox.GroupHeading class="caps px-2 pt-2 pb-1 text-fg-faint">{g.group}</Combobox.GroupHeading>
                {#each g.items as i (i.value)}{@render item(i)}{/each}
              </Combobox.Group>
            {:else}
              {#each g.items as i (i.value)}{@render item(i)}{/each}
            {/if}
          {/each}
          {#if !shown.length}<div class="px-2 py-1.5 text-sm text-fg-faint">Nothing stored matches, the typed value is used as is</div>{/if}
        </Combobox.Viewport>
      </Combobox.Content>
    </Combobox.Portal>
  </Combobox.Root>
</div>

{#snippet item(i: ComboItem)}
  <Combobox.Item value={i.value} label={i.value} class="flex cursor-pointer items-center gap-2 rounded-[5px] px-2 py-1.5 text-sm outline-none select-none data-[highlighted]:bg-raised">
    {#snippet children({ selected })}
      <span class="min-w-0 flex-1 truncate {mono ? 'font-mono' : ''} {selected ? 'text-fg' : 'text-fg-muted'}" title={i.value}>
        {i.label}{#if i.detail}<span class="ml-1.5 text-fg-faint">{i.detail}</span>{/if}
      </span>
      {#if selected}<Check size={13} class="shrink-0 text-accent" />{/if}
    {/snippet}
  </Combobox.Item>
{/snippet}
