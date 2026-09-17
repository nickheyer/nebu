<script lang="ts">
  import { systemMessageItems } from '$lib/gateway';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';

  // How a route shapes requests before the runtime sees them; auto leaves each choice to the instance
  let { value = $bindable('auto'), idPrefix = 'profile' }: { value: string; idPrefix?: string } = $props();
</script>

<div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2 lg:grid-cols-3">
  <Field label="System messages after the first" for="{idPrefix}-system" description="Some chat templates refuse a system message that is not the first message.">
    <Select id="{idPrefix}-system" bind:value items={systemMessageItems} />
  </Field>
</div>
