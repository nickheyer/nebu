<script lang="ts">
  import { enumLabel, tone } from '$lib/format';
  import Badge from './Badge.svelte';

  let { values, value, size = 'sm' }: { values: Record<number, string>; value: number | undefined; size?: 'xs' | 'sm' } = $props();
  const label = $derived(enumLabel(values, value));
  const t = $derived(tone(label));
  const live = $derived(['starting', 'running', 'pending', 'swapping', 'draining', 'stopping'].includes(label));
  const shown = $derived(label.charAt(0).toUpperCase() + label.slice(1));
</script>

<Badge tone={t} dot pulse={live} {size} label={shown} />
