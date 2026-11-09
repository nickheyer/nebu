<script lang="ts">
  import type { NeoEllipsisProps } from '#lib/neo/text/neo-ellipsis.model.js';

  const {
    // Snippets
    children,

    // States
    value,
    lines,

    // Other props
    ...rest
  }: NeoEllipsisProps = $props();
</script>

<span class:neo-ellipsis={true} style:--neo-ellipsis-lines={lines} {...rest}>
  {value}
  {@render children?.()}
</span>

<style lang="scss">
  @use '../styles/layers' as layers;
  @use '../styles/mixin' as mixin;

  @include layers.neo-components {
    .neo-ellipsis {
      @include mixin.ellipsis;
    }
  }
</style>
