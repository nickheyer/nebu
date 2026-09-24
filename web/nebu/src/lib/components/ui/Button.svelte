<script lang="ts" module>
  export type ButtonVariant = 'primary' | 'secondary' | 'subtle' | 'ghost' | 'danger';
  // One height scale for every control. xs sits in table rows, sm in section headers and
  // toolbars beside small inputs, md beside form inputs, lg for a page's single main action.
  export type ButtonSize = 'xs' | 'sm' | 'md' | 'lg';
</script>

<script lang="ts">
  import type { Snippet, Component } from 'svelte';
  import type { HTMLButtonAttributes } from 'svelte/elements';
  import Spinner from './Spinner.svelte';

  // Icon-only buttons require an aria-label.
  let {
    variant = 'secondary',
    size = 'md',
    loading = false,
    icon,
    href,
    children,
    class: cls = '',
    disabled,
    ...rest
  }: {
    variant?: ButtonVariant;
    size?: ButtonSize;
    loading?: boolean;
    icon?: Component<any>;
    href?: string;
    children?: Snippet;
    class?: string;
  } & HTMLButtonAttributes = $props();

  const variants: Record<ButtonVariant, string> = {
    primary: 'bg-accent text-accent-fg font-semibold hover:bg-accent-strong',
    secondary: 'border border-line bg-raised/60 text-fg hover:border-line-strong hover:bg-raised',
    subtle: 'bg-raised/50 text-fg hover:bg-raised',
    ghost: 'text-fg-muted hover:bg-raised hover:text-fg',
    danger: 'border border-bad/25 bg-bad/8 text-bad hover:bg-bad/15'
  };
  const disabledLooks: Record<ButtonVariant, string> = {
    primary: 'bg-raised text-fg-faint font-semibold',
    secondary: 'border border-line bg-transparent text-fg-faint',
    subtle: 'bg-raised/30 text-fg-faint',
    ghost: 'text-fg-faint',
    danger: 'border border-line bg-transparent text-fg-faint'
  };
  const sizes: Record<ButtonSize, string> = { xs: 'h-7 gap-1 text-xs', sm: 'h-8 gap-1.5 text-[13px]', md: 'h-9 gap-2 text-sm', lg: 'h-10 gap-2 text-sm' };
  const pads: Record<ButtonSize, string> = { xs: 'px-2', sm: 'px-2.5', md: 'px-3.5', lg: 'px-4' };
  const squares: Record<ButtonSize, string> = { xs: 'w-7', sm: 'w-8', md: 'w-9', lg: 'w-10' };
  const iconSize: Record<ButtonSize, number> = { xs: 12, sm: 13, md: 14, lg: 16 };
  // Keep the loading state visually distinct from disabled.
  const look = $derived(disabled && !loading ? disabledLooks[variant] : variants[variant]);
  const classes = $derived(
    `relative inline-flex shrink-0 items-center justify-center whitespace-nowrap rounded-md font-medium transition-colors select-none disabled:cursor-not-allowed aria-disabled:pointer-events-none ${loading ? 'cursor-progress' : ''} ${look} ${sizes[size]} ${children ? pads[size] : squares[size]} ${cls}`
  );
</script>

{#snippet inner()}
  {#if loading && !icon}
    <span class="absolute inset-0 flex items-center justify-center"><Spinner size={iconSize[size]} /></span>
    <span class="invisible">{@render children?.()}</span>
  {:else}
    {#if loading}
      <Spinner size={iconSize[size]} />
    {:else if icon}
      {@const Icon = icon}
      <Icon size={iconSize[size]} strokeWidth={2} />
    {/if}
    {@render children?.()}
  {/if}
{/snippet}

{#if href}
  <a {href} class={classes} aria-disabled={disabled || loading}>{@render inner()}</a>
{:else}
  <button class={classes} disabled={disabled || loading} {...rest}>{@render inner()}</button>
{/if}
