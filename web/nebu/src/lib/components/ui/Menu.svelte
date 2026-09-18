<script lang="ts">
  import { DropdownMenu } from 'bits-ui';
  import { goto } from '$app/navigation';
  import { ChevronDown, Ellipsis } from '@lucide/svelte';
  import type { Component } from 'svelte';

  export interface MenuItem {
    label: string;
    icon?: Component<any>;
    onSelect?: () => void;
    href?: string;
    tone?: 'bad' | 'default';
    disabled?: boolean;
    separator?: boolean;
    // A short line under the label
    detail?: string;
  }

  // An ellipsis trigger by default, or a labeled button with a chevron when given a label
  let {
    items,
    label,
    icon,
    variant = 'secondary',
    size = 'md',
    align = 'end',
    onOpenChange
  }: { items: MenuItem[]; label?: string; icon?: Component<any>; variant?: 'primary' | 'secondary' | 'ghost'; size?: 'sm' | 'md'; align?: 'start' | 'end'; onOpenChange?: (open: boolean) => void } = $props();

  const variants = {
    primary: 'bg-accent text-accent-fg font-semibold hover:bg-accent-strong',
    secondary: 'border border-line bg-raised/60 text-fg hover:border-line-strong hover:bg-raised',
    ghost: 'text-fg-muted hover:bg-raised hover:text-fg'
  };
  const Icon = $derived(icon);
</script>

<DropdownMenu.Root {onOpenChange}>
  {#if label}
    <DropdownMenu.Trigger
      class="inline-flex shrink-0 items-center gap-1.5 rounded-md font-medium whitespace-nowrap transition-colors select-none {variants[variant]} {size === 'sm' ? 'h-7 px-2 text-xs' : 'h-8 px-3 text-sm'} data-[state=open]:bg-raised"
      onclick={(e: MouseEvent) => e.stopPropagation()}
    >
      {#if Icon}<Icon size={size === 'sm' ? 13 : 14} />{/if}
      {label}
      <ChevronDown size={13} class="opacity-70" />
    </DropdownMenu.Trigger>
  {:else}
    <DropdownMenu.Trigger
      class="inline-flex shrink-0 items-center justify-center rounded-md text-fg-muted transition-colors hover:bg-raised hover:text-fg data-[state=open]:bg-raised data-[state=open]:text-fg {size === 'sm' ? 'h-7 w-7' : 'h-8 w-8'}"
      aria-label="More"
      onclick={(e: MouseEvent) => e.stopPropagation()}
    >
      <Ellipsis size={15} />
    </DropdownMenu.Trigger>
  {/if}
  <DropdownMenu.Portal>
    <DropdownMenu.Content {align} sideOffset={4} class="enter-up z-[60] min-w-44 rounded-md border border-line bg-overlay p-1 shadow-pop focus:outline-none">
      {#each items as item, i (i)}
        {#if item.separator}
          <DropdownMenu.Separator class="my-1 h-px bg-line" />
        {:else}
          {@const ItemIcon = item.icon}
          <DropdownMenu.Item
            disabled={item.disabled}
            onSelect={() => {
              if (item.href) goto(item.href);
              else item.onSelect?.();
            }}
            class="flex cursor-pointer items-start gap-2.5 rounded-[5px] px-2 py-1.5 text-sm outline-none select-none data-[disabled]:cursor-not-allowed data-[disabled]:opacity-40 data-[highlighted]:bg-raised {item.tone === 'bad' ? 'text-bad' : 'text-fg'}"
          >
            {#if ItemIcon}<ItemIcon size={14} class="mt-0.5 shrink-0 opacity-75" />{/if}
            <span class="min-w-0">
              <span class="block">{item.label}</span>
              {#if item.detail}<span class="block truncate text-xs text-fg-faint">{item.detail}</span>{/if}
            </span>
          </DropdownMenu.Item>
        {/if}
      {/each}
    </DropdownMenu.Content>
  </DropdownMenu.Portal>
</DropdownMenu.Root>
