<script lang="ts">
  import type { NeoIconButtonProps } from '#lib/neo/buttons/neo-icon-button.model.js';

  import NeoButton from '#lib/neo/buttons/NeoButton.svelte';
  import NeoIconCancel from '#lib/neo/icons/NeoIconCancel.svelte';

  let {
    // Button props
    ref = $bindable(),
    checked = $bindable(false),
    hovered = $bindable(false),
    focused = $bindable(false),

    // Other Props
    iconProps,
    ...rest
  }: NeoIconButtonProps = $props();

</script>

{#snippet icon()}
  <NeoIconCancel {...iconProps} />
{/snippet}

<div class="neo-cancel-button">
  <NeoButton bind:ref bind:checked bind:hovered bind:focused {icon} {...rest} />
</div>

<style lang="scss">
  @use '../styles/layers' as layers;

  @include layers.neo-components {
    .neo-cancel-button {
      display: contents;

      > :global(.neo-button) {
        :global(.neo-icon) {
          transition: rotate 0.3s ease;
        }

        &:focus,
        &:hover {
          :global(.neo-icon) {
            transition: rotate 0.3s ease-in-out;
            rotate: -90deg;
          }
        }
      }
    }

  }
</style>
