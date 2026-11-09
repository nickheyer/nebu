<script lang="ts">
  import type { NeoRememberSelectorProps } from '#lib/neo/providers/neo-remember-selector.model.js';

  import NeoButton from '#lib/neo/buttons/NeoButton.svelte';
  import NeoIconSave from '#lib/neo/icons/NeoIconSave.svelte';
  import NeoIconSaveOff from '#lib/neo/icons/NeoIconSaveOff.svelte';
  import { useNeoThemeContext } from '#lib/neo/providers/neo-theme-provider-context.svelte.js';

  const {
    // state
    label = 'Remember',

    // Button props
    ref = $bindable(),
    hovered = $bindable(false),
    focused = $bindable(false),

    // Other props
    ...rest
  }: NeoRememberSelectorProps = $props();

  const context = useNeoThemeContext();
  const remember = $derived(context.remember);
  const onRemember = () => context.update({ remember: !remember });
</script>

<NeoButton
  aria-label="Remember theme settings"
  title="Remember theme settings"
  toggle
  checked={remember}
  onclick={onRemember}
  {label}
  {...rest}
>
  {#snippet icon()}
    {#if remember}
      <NeoIconSave />
    {:else}
      <NeoIconSaveOff />
    {/if}
  {/snippet}
</NeoButton>
