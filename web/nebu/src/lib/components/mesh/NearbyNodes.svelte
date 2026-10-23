<script lang="ts">
  import { UserPlus } from '@lucide/svelte';
  import { clock } from '$lib/state.svelte';
  import { admissionLabel, admissionSettled } from '$lib/mesh';
  import { ago, when } from '$lib/format';
  import type { NearbyNode, Admission } from '$proto/mesh_pb';
  import Section from '$lib/components/ui/Section.svelte';
  import Button from '$lib/components/ui/Button.svelte';

  let {
    nodes,
    waiting,
    member,
    acting,
    oninvite
  }: {
    nodes: NearbyNode[];
    // Admissions this member has under way, matched to nodes by id
    waiting: Admission[];
    // Whether this node is in a mesh and may invite
    member: boolean;
    acting: Set<string>;
    oninvite: (n: NearbyNode) => void;
  } = $props();
</script>

<Section title="Nearby nodes" count={nodes.length}>
  <div class="tbl-wrap">
    <table class="tbl dense">
      <thead>
        <tr><th>Node</th><th>Address</th>{#if !member}<th>Mesh</th>{/if}<th>System</th><th>Last seen</th>{#if member}<th class="actions"></th>{/if}</tr>
      </thead>
      <tbody>
        {#each nodes as n (n.id)}
          {@const a = waiting.find((x) => x.node?.id === n.id)}
          <tr>
            <td class="text-fg" title={n.id}>{n.name || n.id.slice(0, 12)}</td>
            <td class="font-mono text-xs text-fg-muted">{n.address || 'Local only'}</td>
            {#if !member}<td class="text-fg-muted">{n.meshName || '–'}</td>{/if}
            <td class="text-fg-muted">{n.os}/{n.arch}{#if n.version} · nebu {n.version}{/if}</td>
            <td class="text-fg-muted" title={when(n.heardAt)}>{ago(n.heardAt, clock.now)}</td>
            {#if member}
              <td class="actions">
                <div class="flex justify-end">
                  {#if n.meshHash}
                    <span class="text-xs text-fg-muted">Member of {n.meshName}</span>
                  {:else if a && !admissionSettled(a)}
                    <span class="text-xs text-fg-muted">{admissionLabel(a)}</span>
                  {:else}
                    <Button size="sm" icon={UserPlus} loading={acting.has(n.id)} disabled={!n.address} onclick={() => oninvite(n)}>Invite</Button>
                  {/if}
                </div>
              </td>
            {/if}
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  {#if !member && nodes.some((n) => !n.meshHash)}<p class="text-sm text-fg-muted">Create a mesh to invite these nodes.</p>{/if}
</Section>
