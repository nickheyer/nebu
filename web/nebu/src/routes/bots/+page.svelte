<script lang="ts">
  import { goto } from '$app/navigation';
  import { api } from '$lib/api';
  import { live } from '$lib/state.svelte';
  import { fail, ok } from '$lib/toast.svelte';
  import { byName, count } from '$lib/format';
  import { BotState, type Bot } from '$proto/bot_pb';
  import { Bot as BotIcon, Plus } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import Button from '$lib/components/ui/Button.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Switch from '$lib/components/ui/Switch.svelte';
  import SkeletonRows from '$lib/components/ui/SkeletonRows.svelte';

  const bots = $derived([...live.bots.values()].sort(byName((b) => b.name)));
  const loading = $derived(!live.ready && !live.error);
  let busy = $state<string[]>([]);

  async function toggle(b: Bot, on: boolean) {
    busy = [...busy, b.id];
    try {
      if (on) {
        const r = await api.bots.startBot({ id: b.id });
        if (r.bot) live.bots.set(r.bot.id, r.bot);
        ok(`Starting ${b.name}`);
      } else {
        const r = await api.bots.stopBot({ id: b.id });
        if (r.bot) live.bots.set(r.bot.id, r.bot);
        ok(`Stopped ${b.name}`);
      }
    } catch (err) {
      fail(err, on ? 'Start failed' : 'Stop failed');
    } finally {
      busy = busy.filter((id) => id !== b.id);
    }
  }

  const shards = (b: Bot) => {
    const all = b.status?.shards ?? [];
    return all.length ? `${all.filter((s) => s.connected).length}/${all.length}` : '-';
  };
</script>

{#snippet head()}
  <thead>
    <tr>
      <th>Name</th>
      <th>State</th>
      <th>Enabled</th>
      <th class="num">Personas</th>
      <th class="num">Shards</th>
      <th class="num">Guilds</th>
      <th class="num">Replies</th>
      <th class="num">Images</th>
      <th class="num">Videos</th>
      <th class="num">Errors</th>
      <th></th>
    </tr>
  </thead>
{/snippet}

<PageHeader title="Bots">
  <Button variant="primary" icon={Plus} href="/bots/new">New bot</Button>
</PageHeader>

{#if loading}
  <table class="tbl">
    {@render head()}
    <tbody><SkeletonRows rows={3} cols={['w-24', 'w-16', 'w-9', { w: 'w-6', num: true }, { w: 'w-8', num: true }, { w: 'w-6', num: true }, { w: 'w-8', num: true }, { w: 'w-8', num: true }, { w: 'w-8', num: true }, { w: 'w-8', num: true }, 'w-24']} /></tbody>
  </table>
{:else if bots.length === 0}
  <Empty icon={BotIcon} title="No bots">
    <div class="flex max-w-xl flex-col gap-3 text-left text-sm leading-6 text-fg-muted">
      <ol class="list-decimal space-y-1 pl-5">
        <li>Create a <a class="link" href="https://discord.com/developers/applications" target="_blank" rel="noreferrer">Discord application</a>.</li>
        <li>Under Bot, reset and copy the token. Enable Message Content Intent and, for member events, Server Members Intent.</li>
        <li>Add the bot here, then use its invite link to join a server.</li>
      </ol>
      <div><Button size="sm" variant="primary" icon={Plus} href="/bots/new">New bot</Button></div>
    </div>
  </Empty>
{:else}
  <div class="tbl-wrap">
    <table class="tbl">
      {@render head()}
      <tbody>
        {#each bots as b (b.id)}
          <tr class="row-link" onclick={() => goto(`/bots/${b.id}`)}>
            <td class="whitespace-nowrap"><a class="link font-medium" href="/bots/{b.id}" onclick={(e) => e.stopPropagation()}>{b.name}</a></td>
            <td><State values={BotState} value={b.state} /></td>
            <td onclick={(e) => e.stopPropagation()}><Switch checked={b.enabled} label="Enabled" disabled={busy.includes(b.id)} onchange={(on) => toggle(b, on)} /></td>
            <td class="num">{b.spec?.personas.length ?? 0}</td>
            <td class="num">{shards(b)}</td>
            <td class="num">{b.status ? count(b.status.guilds) : '-'}</td>
            <td class="num">{count(b.status?.replies)}</td>
            <td class="num">{count(b.status?.images)}</td>
            <td class="num">{count(b.status?.videos)}</td>
            <td class="num {b.status?.errors ? 'text-bad' : ''}">{count(b.status?.errors)}</td>
            <td class="max-w-xs truncate text-xs text-bad" title={b.error}>{b.state === BotState.FAILED ? b.error : ''}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}
