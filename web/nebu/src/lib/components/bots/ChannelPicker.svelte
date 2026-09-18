<script lang="ts">
  import { api } from '$lib/api';
  import { fail } from '$lib/toast.svelte';
  import { count } from '$lib/format';
  import type { BotGuild } from '$proto/bot_pb';
  import { ListTree } from '@lucide/svelte';
  import Button from '../ui/Button.svelte';
  import Dialog from '../ui/Dialog.svelte';
  import Checkbox from '../ui/Checkbox.svelte';
  import Spinner from '../ui/Spinner.svelte';
  import Empty from '../ui/Empty.svelte';

  let { botId, onPick, guilds = false, size = 'md' }: { botId: string; onPick: (ids: string[]) => void; guilds?: boolean; size?: 'sm' | 'md' } = $props();

  let open = $state(false);
  let loading = $state(false);
  let list = $state<BotGuild[]>([]);
  let chosen = $state<string[]>([]);

  const postable = new Set(['text', 'news', 'thread']);

  async function show() {
    open = true;
    chosen = [];
    loading = true;
    try {
      const r = await api.bots.listBotGuilds({ botId });
      list = r.guilds;
    } catch (err) {
      fail(err, 'Could not list guilds');
      open = false;
    } finally {
      loading = false;
    }
  }

  function toggle(id: string, on: boolean) {
    chosen = on ? [...chosen.filter((x) => x !== id), id] : chosen.filter((x) => x !== id);
  }

  function done() {
    onPick(chosen);
    open = false;
  }
</script>

<Button type="button" {size} icon={ListTree} onclick={show}>Pick</Button>

<Dialog bind:open title={guilds ? 'Pick guilds' : 'Pick channels'}>
  {#if loading}
    <div class="flex items-center gap-2 py-6 text-sm text-fg-muted"><Spinner size={15} /> Loading</div>
  {:else if list.length === 0}
    <Empty compact title="No guilds. Invite the bot from its overview." />
  {:else}
    <div class="flex flex-col gap-4">
      {#each list as g (g.id)}
        {#if guilds}
          <Checkbox label={g.name} hint="{g.id} ({count(g.members)} members)" checked={chosen.includes(g.id)} onchange={(on) => toggle(g.id, on)} />
        {:else}
          {@const channels = g.channels.filter((c) => postable.has(c.kind))}
          <div class="flex flex-col gap-1">
            <div class="flex items-baseline gap-2">
              <span class="text-sm font-medium text-fg">{g.name}</span>
              <span class="font-mono text-xs text-fg-faint">{g.id}</span>
            </div>
            {#if channels.length === 0}
              <span class="text-xs text-fg-faint">No visible text channels</span>
            {:else}
              <div class="grid grid-cols-1 sm:grid-cols-2">
                {#each channels as c (c.id)}
                  <Checkbox label="#{c.name}" hint="{c.id}{c.kind === 'text' ? '' : ` (${c.kind})`}" checked={chosen.includes(c.id)} onchange={(on) => toggle(c.id, on)} />
                {/each}
              </div>
            {/if}
          </div>
        {/if}
      {/each}
    </div>
  {/if}
  {#snippet footer()}
    <span class="text-xs text-fg-faint">{chosen.length} selected</span>
    <Button variant="ghost" class="ml-auto" onclick={() => (open = false)}>Cancel</Button>
    <Button variant="primary" disabled={chosen.length === 0} onclick={done}>Add</Button>
  {/snippet}
</Dialog>
