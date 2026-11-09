<script lang="ts">
  import type { NeoArrowButtonProps } from '#lib/neo/buttons/neo-arrow-button.model.js';

  import NeoButton from '#lib/neo/buttons/NeoButton.svelte';
  import { NeoIconArrowDirection } from '#lib/neo/icons/neo-icon.model.js';
  import NeoIconArrow from '#lib/neo/icons/NeoIconArrow.svelte';

  let {
    // State
    direction = NeoIconArrowDirection.Right,

    // Button props
    ref = $bindable(),
    checked = $bindable(false),
    hovered = $bindable(false),
    focused = $bindable(false),
    disabled,
    readonly,

    // Other Props
    arrowProps,
    ...rest
  }: NeoArrowButtonProps = $props();

  const reverse = $derived(direction === 'right');
</script>

{#snippet icon()}
  <NeoIconArrow {direction} expanded={(checked || hovered || focused) && !disabled && !readonly} {...arrowProps} />
{/snippet}

<NeoButton bind:ref bind:checked bind:hovered bind:focused {icon} {reverse} {disabled} {readonly} {...rest} />
