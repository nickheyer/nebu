<script lang="ts">
  import type { Snippet, Component } from 'svelte';
  import type { HTMLButtonAttributes } from 'svelte/elements';
  import Spinner from './Spinner.svelte';

  type Variant = 'primary' | 'secondary' | 'ghost' | 'danger';
  type Size = 'sm' | 'md' | 'lg';

  // A button without children is square and shows only its icon, so it needs an aria-label
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
    variant?: Variant;
    size?: Size;
    loading?: boolean;
    icon?: Component<any>;
    href?: string;
    children?: Snippet;
    class?: string;
  } & HTMLButtonAttributes = $props();

  const variants: Record<Variant, string> = {
    primary: 'bg-accent text-accent-fg font-semibold hover:bg-accent-strong',
    secondary: 'border border-line bg-raised text-fg hover:border-line-strong',
    ghost: 'text-fg-muted hover:bg-raised hover:text-fg',
    danger: 'border border-bad/30 bg-bad/10 text-bad hover:bg-bad/20'
  };
  const sizes: Record<Size, string> = { sm: 'h-8 gap-1.5 text-sm', md: 'h-9 gap-2 text-sm', lg: 'h-10 gap-2 text-sm' };
  const pads: Record<Size, string> = { sm: 'px-2.5', md: 'px-3.5', lg: 'px-4' };
  const squares: Record<Size, string> = { sm: 'w-8', md: 'w-9', lg: 'w-10' };
  const iconSize: Record<Size, number> = { sm: 14, md: 16, lg: 16 };
  const classes = $derived(
    `inline-flex shrink-0 items-center justify-center whitespace-nowrap rounded-lg font-medium transition-colors select-none disabled:opacity-50 ${variants[variant]} ${sizes[size]} ${children ? pads[size] : squares[size]} ${cls}`
  );
</script>

{#snippet inner()}
  {#if loading}
    <Spinner size={iconSize[size]} />
  {:else if icon}
    {@const Icon = icon}
    <Icon size={iconSize[size]} strokeWidth={2} />
  {/if}
  {@render children?.()}
{/snippet}

{#if href}
  <a {href} class={classes} aria-disabled={disabled || loading}>{@render inner()}</a>
{:else}
  <button class={classes} disabled={disabled || loading} {...rest}>{@render inner()}</button>
{/if}
