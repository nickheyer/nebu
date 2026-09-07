<script lang="ts">
  import type { Policy } from '$proto/gateway_pb';
  import type { PolicyFields } from '$lib/gateway';
  import Field from './ui/Field.svelte';
  import NumberInput from './ui/NumberInput.svelte';

  // The limits a route enforces; an empty field inherits the gateway default, shown in the field as what applies
  let { fields = $bindable(), defaults, idPrefix = 'policy' }: { fields: PolicyFields; defaults: Policy | undefined; idPrefix?: string } = $props();

  const none = 'unlimited';
  const seconds = (ms: number | undefined) => (ms ? String(ms / 1000) : none);
  const burstDefault = $derived(defaults?.burst ? String(defaults.burst) : defaults?.requestsPerSecond ? String(Math.ceil(defaults.requestsPerSecond)) : '= rate');
</script>

<div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2 lg:grid-cols-3">
  <Field label="Requests in flight" for="{idPrefix}-inflight" description="Concurrent requests the route accepts. More get 429.">
    <NumberInput id="{idPrefix}-inflight" integer min={0} unit="requests" bind:value={fields.maxInFlight} fallback={defaults?.maxInFlight ? String(defaults.maxInFlight) : none} />
  </Field>
  <Field label="Rate" for="{idPrefix}-rps" description="Sustained requests per second.">
    <NumberInput id="{idPrefix}-rps" min={0} step={0.5} unit="req/s" bind:value={fields.rps} fallback={defaults?.requestsPerSecond ? String(defaults.requestsPerSecond) : none} />
  </Field>
  <Field label="Burst" for="{idPrefix}-burst" description="Requests absorbed at once before the rate applies.">
    <NumberInput id="{idPrefix}-burst" integer min={0} unit="requests" bind:value={fields.burst} fallback={burstDefault} />
  </Field>
  <Field label="Request timeout" for="{idPrefix}-timeout" description="Whole exchange, first byte in to last byte out.">
    <NumberInput id="{idPrefix}-timeout" min={0} step={5} unit="s" bind:value={fields.timeout} fallback={seconds(defaults?.requestTimeoutMs)} />
  </Field>
  <Field label="Time to first byte" for="{idPrefix}-upstream" description="How long the runtime may take to start answering.">
    <NumberInput id="{idPrefix}-upstream" min={0} step={5} unit="s" bind:value={fields.upstream} fallback={seconds(defaults?.upstreamTimeoutMs)} />
  </Field>
</div>
