<script lang="ts" generics="Id extends TabId, Value = any">
  import type { TabId } from '#lib/neo/index.js';

  import type { NeoTabsCardProps } from '#lib/neo/nav/neo-tabs-card.model.js';

  import NeoCard from '#lib/neo/cards/NeoCard.svelte';
  import NeoTransitionContainer from '#lib/neo/containers/NeoTransitionContainer.svelte';
  import { setTabsCardContext } from '#lib/neo/nav/neo-tabs-card.model.js';
  import { getTabContext } from '#lib/neo/nav/neo-tabs-context.svelte.js';

  let {
    // Snippets
    children,

    // States
    ref = $bindable(),

    // Styles
    animate = true,

    // Other props
    containerProps,
    ...rest
  }: NeoTabsCardProps<Id, Value> = $props();

  const context = getTabContext<Id, Value>();
  const borderless = $derived(context?.state?.borderless);
  const elevation = $derived(context?.state?.elevation);
  const pressed = $derived(context?.state?.pressed);
  const convex = $derived(context?.state?.convex);
  const glass = $derived(context?.state?.glass);
  const start = $derived(context?.state?.start);

  $effect.pre(() => {
    setTabsCardContext({ animate });
  });
</script>

<NeoCard bind:ref {borderless} {elevation} {pressed} {convex} {glass} {start} {...rest}>
  {#if animate}
    <NeoTransitionContainer overflow="hidden" {...containerProps}>
      {@render children?.(context?.state)}
    </NeoTransitionContainer>
  {:else}
    {@render children?.(context?.state)}
  {/if}
</NeoCard>
