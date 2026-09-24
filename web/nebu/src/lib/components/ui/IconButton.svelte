<script lang="ts">
  import type { Component } from 'svelte';
  import type { HTMLButtonAttributes } from 'svelte/elements';
  import Button, { type ButtonSize, type ButtonVariant } from './Button.svelte';
  import Tip from './Tip.svelte';

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
    size?: ButtonSize;
    variant?: ButtonVariant;
    loading?: boolean;
    href?: string;
    class?: string;
  } & HTMLButtonAttributes = $props();
</script>

<Tip text={label}>
  <Button {icon} {size} {variant} {loading} {href} class={cls} aria-label={label} {...rest} />
</Tip>
