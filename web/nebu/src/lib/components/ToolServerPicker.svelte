<script lang="ts">
  import { onMount } from 'svelte';
  import { api, message } from '$lib/api';
  import { live, nodeName, inMesh } from '$lib/state.svelte';
  import { kindLabel, stateLabel, toolServers } from '$lib/tools';
  import type { ToolServerStatus } from '$proto/tools_pb';
  import Checkbox from './ui/Checkbox.svelte';
  import SwitchRow from './ui/SwitchRow.svelte';

  // Picks the tool servers an attachment names, this node's and every member's, by their refs
  let { refs = $bindable([]), mutating = $bindable(false) }: { refs: string[]; mutating: boolean } = $props();

  let remote = $state<ToolServerStatus[]>([]);
  let remoteError = $state('');

  const choices = $derived([...toolServers(), ...remote]);
  // Refs no listed server answers to, kept so a member that is away does not lose its attachment
  const orphans = $derived(refs.filter((r) => !choices.some((c) => c.ref === r)));

  onMount(async () => {
    if (!inMesh()) return;
    try {
      const r = await api.tools.listToolServers({ mesh: true });
      remote = r.servers.filter((s) => s.nodeId);
    } catch (err) {
      remoteError = message(err);
    }
  });

  function toggle(ref: string, on: boolean) {
    refs = on ? [...refs.filter((r) => r !== ref), ref] : refs.filter((r) => r !== ref);
  }

  const toolsText = (s: ToolServerStatus) => (s.tools.length ? s.tools.map((t) => t.name).join(', ') : stateLabel(s.state));
</script>

<div class="flex flex-col gap-2">
  {#if !choices.length && !orphans.length}
    <p class="text-sm text-fg-faint">No tool servers in <a class="link" href="/settings">Settings</a> yet.</p>
  {:else}
    <div class="flex flex-col rounded-md border border-line px-3 py-1">
      {#each choices as s (s.ref)}
        <Checkbox checked={refs.includes(s.ref)} onchange={(on) => toggle(s.ref, on)} label={s.nodeId ? `${s.server?.name} on ${nodeName(s.nodeId)}` : (s.server?.name ?? '')} hint="{kindLabel(s.server?.kind ?? 0)} · {toolsText(s)}" />
      {/each}
      {#each orphans as ref (ref)}
        <Checkbox checked={true} onchange={(on) => toggle(ref, on)} label={ref} hint="Not reachable right now" />
      {/each}
    </div>
    {#if remoteError}<p class="text-xs text-warn">Members' servers could not be listed: {remoteError}</p>{/if}
    {#if refs.length}
      <SwitchRow bind:checked={mutating} label="Mutating tools" description="Also offer tools that change things." />
    {/if}
  {/if}
</div>

