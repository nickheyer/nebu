<script lang="ts">
  import type { NeoPortalProps } from '#lib/neo/floating/portal/neo-portal.model.js';

  import { mount, unmount, untrack } from 'svelte';

  import { getNeoPortalContext } from '#lib/neo/floating/portal/neo-portal-context.svelte.js';
  import NeoPortalTarget from '#lib/neo/floating/portal/NeoPortalTarget.svelte';

  const { enabled = false, children, outro = true, target = document.body, anchor, ...rest }: NeoPortalProps = $props();

  const portalContext = getNeoPortalContext();

  // Each holds its value while a parent update leaves it unchanged, so the content mounts once and stays
  const active = $derived(enabled && !!children);
  const host = $derived(target);
  const before = $derived(anchor ?? portalContext?.ref);

  let component: ReturnType<typeof mount>;
  $effect.pre(() => {
    if (!active) return;
    const place = { target: host, anchor: before };
    component = untrack(() => mount(NeoPortalTarget, { ...place, props: { children: children! }, ...rest }));

    return () => {
      if (!component) return;
      unmount(component, { outro });
    };
  });
</script>

{#if !enabled && children}
  {@render children()}
{/if}
