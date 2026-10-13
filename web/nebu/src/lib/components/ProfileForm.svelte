<script lang="ts">
  import { systemMessageItems, type ProfileFields } from '$lib/gateway';
  import Field from './ui/Field.svelte';
  import Select from './ui/Select.svelte';
  import SwitchRow from './ui/SwitchRow.svelte';

  let { fields = $bindable(), idPrefix = 'profile' }: { fields: ProfileFields; idPrefix?: string } = $props();
</script>

<div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2 lg:grid-cols-3">
  <Field label="System messages after the first" for="{idPrefix}-system">
    <Select id="{idPrefix}-system" bind:value={fields.systemMessages} items={systemMessageItems} />
  </Field>
  <Field label="Coding agent approvals" for="{idPrefix}-allow-all">
    <div id="{idPrefix}-allow-all">
      <SwitchRow bind:checked={fields.allowAll} label="Allow all" description="Answer permission classifier prompts with the allow verdict without running the model." />
    </div>
  </Field>
</div>
