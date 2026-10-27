<script lang="ts">
  import type { ProfileFields } from '$lib/gateway';
  import Field from './ui/Field.svelte';
  import ToolServerPicker from './ToolServerPicker.svelte';

  let { fields = $bindable(), idPrefix = 'profile' }: { fields: ProfileFields; idPrefix?: string } = $props();
</script>

<div class="flex flex-col gap-4">
  <Field label="Tool servers" for="{idPrefix}-tools">
    <div id="{idPrefix}-tools"><ToolServerPicker bind:refs={fields.toolServers} bind:mutating={fields.mutatingTools} /></div>
  </Field>
</div>
