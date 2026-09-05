<script lang="ts">
  import { DropdownMenu } from 'bits-ui';
  import { goto } from '$app/navigation';
  import { Ellipsis } from '@lucide/svelte';
  import type { Component } from 'svelte';

  export interface MenuItem {
    label: string;
    icon?: Component<any>;
    onSelect?: () => void;
    href?: string;
    tone?: 'bad' | 'default';
    disabled?: boolean;
    separator?: boolean;
  }

  let { items }: { items: MenuItem[] } = $props();
</script>

<DropdownMenu.Root>
  <DropdownMenu.Trigger
    class="inline-flex h-7 w-7 items-center justify-center rounded-md text-fg-muted transition-colors hover:bg-raised hover:text-fg data-[state=open]:bg-raised data-[state=open]:text-fg"
    aria-label="Actions"
    onclick={(e: MouseEvent) => e.stopPropagation()}
  >
    <Ellipsis size={16} />
  </DropdownMenu.Trigger>
  <DropdownMenu.Portal>
    <DropdownMenu.Content align="end" sideOffset={4} class="enter-up z-[60] min-w-44 rounded-lg border border-line bg-overlay p-1 shadow-pop focus:outline-none">
      {#each items as item, i (i)}
        {#if item.separator}
          <DropdownMenu.Separator class="my-1 h-px bg-line" />
        {:else}
          {@const Icon = item.icon}
          <DropdownMenu.Item
            disabled={item.disabled}
            onSelect={() => {
              if (item.href) goto(item.href);
              else item.onSelect?.();
            }}
            class="flex cursor-pointer items-center gap-2.5 rounded-md px-2.5 py-1.5 text-sm outline-none select-none data-[disabled]:opacity-40 data-[highlighted]:bg-raised {item.tone === 'bad'
              ? 'text-bad'
              : 'text-fg'}"
          >
            {#if Icon}<Icon size={14} class="shrink-0 opacity-80" />{/if}
            {item.label}
          </DropdownMenu.Item>
        {/if}
      {/each}
    </DropdownMenu.Content>
  </DropdownMenu.Portal>
</DropdownMenu.Root>
