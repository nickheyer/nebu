<script lang="ts">
  import type { Snippet, Component } from 'svelte';
  import type { HTMLButtonAttributes } from 'svelte/elements';
  import Spinner from './Spinner.svelte';

  type Variant = 'default' | 'primary' | 'ghost' | 'danger' | 'outline';
  type Size = 'xs' | 'sm' | 'md' | 'lg';

  let {
    variant = 'default',
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
    default: 'border-line bg-raised text-fg hover:border-line-strong hover:bg-raised/80',
    outline: 'border-line bg-transparent text-fg hover:bg-raised/60',
    primary: 'border-accent-strong bg-accent text-accent-fg hover:bg-accent-strong font-semibold',
    ghost: 'border-transparent bg-transparent text-fg-muted hover:bg-raised hover:text-fg',
    danger: 'border-bad/40 bg-bad/10 text-bad hover:bg-bad/20'
  };
  const sizes: Record<Size, string> = {
    xs: 'h-6 px-2 text-xs gap-1 rounded',
    sm: 'h-7 px-2.5 text-xs gap-1.5',
    md: 'h-8 px-3 text-sm gap-2',
    lg: 'h-10 px-4 text-sm gap-2'
  };
  const iconSize: Record<Size, number> = { xs: 12, sm: 13, md: 15, lg: 16 };
  const classes = $derived(
    `inline-flex shrink-0 items-center justify-center whitespace-nowrap rounded-md border transition-colors select-none disabled:opacity-50 ${variants[variant]} ${sizes[size]} ${cls}`
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
