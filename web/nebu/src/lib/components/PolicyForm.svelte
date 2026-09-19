<script lang="ts">
  import type { Policy } from '$proto/gateway_pb';
  import type { PolicyFields } from '$lib/gateway';
  import Field from './ui/Field.svelte';
  import NumberInput from './ui/NumberInput.svelte';

  let { fields = $bindable(), defaults, idPrefix = 'policy' }: { fields: PolicyFields; defaults: Policy | undefined; idPrefix?: string } = $props();

  const none = '0';
  const seconds = (ms: number | undefined) => (ms ? String(ms / 1000) : none);
  const burstDefault = $derived(defaults?.burst ? String(defaults.burst) : defaults?.requestsPerSecond ? String(Math.ceil(defaults.requestsPerSecond)) : none);
</script>

<div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2 lg:grid-cols-3">
  <Field label="Requests in flight" for="{idPrefix}-inflight" description="Maximum concurrent requests. Excess requests return 429.">
    <NumberInput id="{idPrefix}-inflight" integer min={0} unit="requests" bind:value={fields.maxInFlight} empty={defaults?.maxInFlight ? String(defaults.maxInFlight) : none} />
  </Field>
  <Field label="Rate" for="{idPrefix}-rps" description="Sustained requests per second.">
    <NumberInput id="{idPrefix}-rps" min={0} step={0.5} unit="req/s" bind:value={fields.rps} empty={defaults?.requestsPerSecond ? String(defaults.requestsPerSecond) : none} />
  </Field>
  <Field label="Burst" for="{idPrefix}-burst" description="Requests allowed in a burst.">
    <NumberInput id="{idPrefix}-burst" integer min={0} unit="requests" bind:value={fields.burst} empty={burstDefault} />
  </Field>
  <Field label="Request timeout" for="{idPrefix}-timeout" description="Whole exchange, first byte in to last byte out.">
    <NumberInput id="{idPrefix}-timeout" min={0} step={5} unit="s" bind:value={fields.timeout} empty={seconds(defaults?.requestTimeoutMs)} />
  </Field>
  <Field label="Time to first byte" for="{idPrefix}-upstream" description="Maximum wait for the runtime response.">
    <NumberInput id="{idPrefix}-upstream" min={0} step={5} unit="s" bind:value={fields.upstream} empty={seconds(defaults?.upstreamTimeoutMs)} />
  </Field>
</div>
