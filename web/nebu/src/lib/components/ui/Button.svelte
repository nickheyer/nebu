<script lang="ts">
  import type { Snippet, Component } from 'svelte';
  import type { HTMLButtonAttributes } from 'svelte/elements';
  import Spinner from './Spinner.svelte';

  type Variant = 'primary' | 'secondary' | 'subtle' | 'ghost' | 'danger';
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
    secondary: 'border border-line bg-raised/60 text-fg hover:border-line-strong hover:bg-raised',
    subtle: 'bg-raised/50 text-fg hover:bg-raised',
    ghost: 'text-fg-muted hover:bg-raised hover:text-fg',
    danger: 'border border-bad/25 bg-bad/8 text-bad hover:bg-bad/15'
  };
  // A disabled button keeps its shape and loses its color, so it stays legible on the dark ground
  const disabledLooks: Record<Variant, string> = {
    primary: 'bg-raised text-fg-faint font-semibold',
    secondary: 'border border-line bg-transparent text-fg-faint',
    subtle: 'bg-raised/30 text-fg-faint',
    ghost: 'text-fg-faint',
    danger: 'border border-line bg-transparent text-fg-faint'
  };
  const sizes: Record<Size, string> = { sm: 'h-7 gap-1.5 text-xs', md: 'h-8 gap-1.5 text-sm', lg: 'h-9 gap-2 text-sm' };
  const pads: Record<Size, string> = { sm: 'px-2', md: 'px-3', lg: 'px-3.5' };
  const squares: Record<Size, string> = { sm: 'w-7', md: 'w-8', lg: 'w-9' };
  const iconSize: Record<Size, number> = { sm: 13, md: 14, lg: 15 };
  // While loading the button keeps its look, so a spinner never sits on a greyed out button
  const look = $derived(disabled && !loading ? disabledLooks[variant] : variants[variant]);
  const classes = $derived(
    `inline-flex shrink-0 items-center justify-center whitespace-nowrap rounded-md font-medium transition-colors select-none disabled:cursor-not-allowed aria-disabled:pointer-events-none ${loading ? 'cursor-progress' : ''} ${look} ${sizes[size]} ${children ? pads[size] : squares[size]} ${cls}`
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
