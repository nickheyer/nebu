<script lang="ts">
  import type { Component } from 'svelte';
  import Button from './Button.svelte';
  import IconButton from './IconButton.svelte';

  let {
    icon,
    label,
    armed: armedLabel,
    size = 'xs',
    compact = false,
    loading = false,
    disabled = false,
    onconfirm,
    class: cls = ''
  }: { icon?: Component<any>; label: string; armed: string; size?: 'xs' | 'sm' | 'md'; compact?: boolean; loading?: boolean; disabled?: boolean; onconfirm: () => void; class?: string } = $props();

  let armed = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  function disarm() {
    if (timer) clearTimeout(timer);
    timer = undefined;
    armed = false;
  }

  function click() {
    if (!armed) {
      armed = true;
      timer = setTimeout(disarm, 4000);
      return;
    }
    disarm();
    onconfirm();
  }
</script>

{#if armed}
  <Button variant="danger" {size} {icon} {loading} class={cls} onclick={click} onblur={disarm} onkeydown={(e) => e.key === 'Escape' && disarm()} {@attach (el: HTMLElement) => el.focus()}>{armedLabel}</Button>
{:else if compact && icon}
  <IconButton {icon} {label} {size} {loading} {disabled} class={cls} onclick={click} />
{:else}
  <Button variant="ghost" {size} {icon} {loading} {disabled} class={cls} onclick={click}>{label}</Button>
{/if}
