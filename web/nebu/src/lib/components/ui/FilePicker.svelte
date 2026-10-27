<script lang="ts">
  import { FolderOpen } from '@lucide/svelte';
  import TextInput from './TextInput.svelte';
  import Button from './Button.svelte';
  import DirBrowser from './DirBrowser.svelte';

  // A path typed or picked from the host, executables alone by default
  let { value = $bindable(''), id, empty = '', mode = 'executables', class: cls = '' }: { value?: string; id?: string; empty?: string; mode?: 'files' | 'executables'; class?: string } = $props();

  let open = $state(false);
</script>

<div class="flex flex-col gap-2 {cls}">
  <div class="flex gap-2">
    <TextInput {id} mono class="flex-1" {empty} bind:value />
    <Button type="button" variant={open ? 'subtle' : 'secondary'} icon={FolderOpen} onclick={() => (open = !open)}>Browse</Button>
  </div>
  {#if open}
    <DirBrowser
      start={value.trim() || empty}
      {mode}
      onpick={(path) => {
        value = path;
        open = false;
      }}
    />
  {/if}
</div>
