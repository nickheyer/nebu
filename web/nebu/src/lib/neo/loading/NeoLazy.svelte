<script lang="ts">
  import type { NeoLazyProps } from '#lib/neo/loading/neo-lazy.model.js';

  import NeoSuspense from '#lib/neo/loading/NeoSuspense.svelte';

  const { component, props: _props, children, ...rest }: NeoLazyProps = $props();
</script>

<NeoSuspense promise={component} {...rest}>
  {#snippet result(Component)}
    <Component {..._props}>
      {@render children?.()}
    </Component>
  {/snippet}
</NeoSuspense>
