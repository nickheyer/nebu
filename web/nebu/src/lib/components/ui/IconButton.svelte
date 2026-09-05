<script lang="ts">
  import type { Component } from 'svelte';
  import type { HTMLButtonAttributes } from 'svelte/elements';
  import Button from './Button.svelte';
  import Tip from './Tip.svelte';

  // A square button whose label lives in its tooltip
  let {
    icon,
    label,
    size = 'md',
    variant = 'ghost',
    loading = false,
    href,
    class: cls = '',
    ...rest
  }: {
    icon: Component<any>;
    label: string;
    size?: 'sm' | 'md' | 'lg';
    variant?: 'primary' | 'secondary' | 'subtle' | 'ghost' | 'danger';
    loading?: boolean;
    href?: string;
    class?: string;
  } & HTMLButtonAttributes = $props();
</script>

<Tip text={label}>
  <Button {icon} {size} {variant} {loading} {href} class={cls} aria-label={label} {...rest} />
</Tip>
