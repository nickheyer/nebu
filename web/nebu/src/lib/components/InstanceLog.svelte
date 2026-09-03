<script lang="ts">
  import { api, message } from '$lib/api';

  let { id, follow = true }: { id: string; follow?: boolean } = $props();
  let lines = $state<string[]>([]);
  let error = $state('');
  let box: HTMLDivElement | undefined = $state();

  $effect(() => {
    const controller = new AbortController();
    lines = [];
    error = '';
    (async () => {
      try {
        for await (const msg of api.instances.logs({ id, follow, tail: 500 }, { signal: controller.signal })) {
          lines = [...lines.slice(-4000), ...msg.lines];
          queueMicrotask(() => box?.scrollTo({ top: box.scrollHeight }));
        }
      } catch (err) {
        if (!controller.signal.aborted) error = message(err);
      }
    })();
    return () => controller.abort();
  });
</script>

{#if error}<div class="text-sm text-red-300">{error}</div>{/if}
<div class="log" bind:this={box}>{lines.join('\n')}</div>
