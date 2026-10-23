<script lang="ts">
  import { bytes } from '$lib/format';
  import type { Sent } from '$lib/chatClient';
  import Copy from '$lib/components/ui/Copy.svelte';
  import Json from '$lib/components/ui/Json.svelte';

  let { sent }: { sent: Sent | null } = $props();

  // Replaces image bytes with their size
  function elide(body: string): string {
    return body.replace(/[A-Za-z0-9+/]{256,}={0,2}(?=")/g, (m) => `…${bytes((m.length * 3) / 4, 0)} of image data…`);
  }

  const shownBody = $derived(sent ? elide(sent.body) : '');
</script>

{#if sent}
  <div class="flex flex-col gap-4">
    <div class="grid grid-cols-2 gap-3">
      <div class="stat"><dt>Path</dt><dd class="font-mono wrap-anywhere">{sent.path}</dd></div>
      <div class="stat"><dt>Status</dt><dd>{sent.status}</dd></div>
    </div>
    {#if Object.keys(sent.headers ?? {}).length}
      <div>
        <div class="caps mb-2 text-fg-faint">Headers</div>
        <pre class="code whitespace-pre-wrap">{Object.entries(sent.headers).map(([k, v]) => `${k}: ${k.toLowerCase() === 'authorization' || k.toLowerCase() === 'x-api-key' ? '••••' : v}`).join('\n')}</pre>
      </div>
    {/if}
    <div>
      <div class="mb-2 flex items-center gap-2">
        <span class="caps text-fg-faint">Body</span>
        {#if shownBody !== sent.body}<span class="text-xs text-fg-faint">image bytes shortened</span><Copy text={sent.body} size={12} class="h-6 w-6" label="Copy the exact body" />{/if}
      </div>
      <Json text={shownBody} height="max-h-[32rem]" />
    </div>
  </div>
{:else}
  <p class="text-sm text-fg-faint">No request sent yet</p>
{/if}
