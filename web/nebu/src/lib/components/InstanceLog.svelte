<script lang="ts">
  import { api, message } from '$lib/api';
  import LogView from './ui/LogView.svelte';

  let { id, follow = true, height = 'h-96' }: { id: string; follow?: boolean; height?: string } = $props();
  let lines = $state<string[]>([]);
  let error = $state('');

  $effect(() => {
    const controller = new AbortController();
    const current = id;
    const f = follow;
    lines = [];
    error = '';
    (async () => {
      try {
        for await (const msg of api.instances.logs({ id: current, follow: f, tail: 1000 }, { signal: controller.signal })) {
          lines.push(...msg.lines);
          if (lines.length > 5000) lines.splice(0, lines.length - 5000);
        }
      } catch (err) {
        if (!controller.signal.aborted) error = message(err);
      }
    })();
    return () => controller.abort();
  });
</script>

{#if error}<div class="note note-bad mb-3">{error}</div>{/if}
<LogView {lines} {height} live={follow} empty="No output yet" />
