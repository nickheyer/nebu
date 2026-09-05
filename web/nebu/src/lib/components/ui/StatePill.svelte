<script lang="ts">
  import { enumLabel, tone } from '$lib/format';
  import Pill from './Pill.svelte';

  // A generated enum value as a pill, pulsing while it names something in motion
  let { values, value, class: cls = '' }: { values: Record<number, string>; value: number | undefined; class?: string } = $props();
  const label = $derived(enumLabel(values, value));
  const moving = $derived(['starting', 'running', 'pending', 'swapping', 'draining', 'stopping'].includes(label));
</script>

<Pill tone={tone(label)} dot pulse={moving} label={label.charAt(0).toUpperCase() + label.slice(1)} class={cls} />
