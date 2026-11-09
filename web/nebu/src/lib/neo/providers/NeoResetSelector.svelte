<script lang="ts">
  import type { NeoResetSelectorProps } from '#lib/neo/providers/neo-reset-selector.model.js';

  import NeoButton from '#lib/neo/buttons/NeoButton.svelte';
  import NeoIconImage from '#lib/neo/icons/NeoIconImage.svelte';
  import { useNeoThemeContext } from '#lib/neo/providers/neo-theme-provider-context.svelte.js';

  const {
    // state
    label = 'Reset',

    // Button props
    ref = $bindable(),
    hovered = $bindable(false),
    focused = $bindable(false),

    // Other props
    ...rest
  }: NeoResetSelectorProps = $props();

  const context = useNeoThemeContext();

  const reset = $derived(context.reset);

  const onReset = () => context.update({ reset: !reset });
</script>

<NeoButton
  aria-label="Toggle {reset ? 'off' : 'on'} style reset"
  title="Toggle {reset ? 'off' : 'on'} style reset"
  toggle
  checked={reset}
  onclick={onReset}
  {label}
  {...rest}
>
  {#snippet icon()}
    <NeoIconImage />
  {/snippet}
</NeoButton>
