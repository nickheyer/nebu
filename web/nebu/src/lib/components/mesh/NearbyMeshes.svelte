<script lang="ts">
  import { Network, Check, X, Send, Trash2 } from '@lucide/svelte';
  import { clock } from '$lib/state.svelte';
  import { admissionLabel, admissionTone, admissionWhat, admissionPending, admissionSettled } from '$lib/mesh';
  import { ago, plural, when } from '$lib/format';
  import { AdmissionState, type NearbyMesh, type Admission } from '$proto/mesh_pb';
  import Section from '$lib/components/ui/Section.svelte';
  import Empty from '$lib/components/ui/Empty.svelte';
  import Card from '$lib/components/ui/Card.svelte';
  import Chip from '$lib/components/ui/Chip.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Button from '$lib/components/ui/Button.svelte';

  // A mesh heard on the network or one that invited this node, with the admission under way
  export interface Candidate {
    hash: string;
    mesh?: NearbyMesh;
    admission?: Admission;
  }

  let {
    candidates,
    announce,
    acting,
    onask,
    onrefuse,
    ondismiss
  }: {
    candidates: Candidate[];
    announce: boolean;
    acting: Set<string>;
    onask: (hash: string) => void;
    onrefuse: (a: Admission) => void;
    ondismiss: (a: Admission) => void;
  } = $props();
</script>

<Section title="Nearby meshes" count={candidates.length || undefined}>
  {#if candidates.length === 0}
    <Empty compact icon={Network} title={announce ? 'No meshes found' : 'Network discovery is off'} description="Create a mesh, or join one with a member address or token." />
  {:else}
    <div class="flex flex-col gap-4">
      {#each candidates as c (c.hash)}
        {@const a = c.admission}
        {@const name = c.mesh?.name || a?.meshName || c.hash.slice(0, 12)}
        {@const members = c.mesh?.members || a?.meshMembers || 0}
        {@const tls = c.mesh?.tls ?? a?.meshTls ?? false}
        <Card title={name} meta={`${plural(members, 'member')} · ${tls ? 'TLS enabled' : 'TLS disabled'}`}>
          {#snippet actions()}
            {#if a}<State tone={admissionTone(a.state)} label={admissionLabel(a)} pulse={a.state === AdmissionState.ADMITTED} />{/if}
          {/snippet}
          <div class="flex flex-col gap-3">
            {#if c.mesh}
              <div class="flex flex-wrap items-center gap-1.5 text-xs text-fg-muted">
                <span>Members</span>
                {#each c.mesh.nodes as n (n.id)}<Chip text={n.name || n.id.slice(0, 8)} mono={false} title={n.address} />{/each}
                <span class="text-fg-faint" title={when(c.mesh.heardAt)}>{ago(c.mesh.heardAt, clock.now)}</span>
              </div>
            {:else if a?.node}
              <div class="text-xs text-fg-muted">Member: {a.node.name} <span class="font-mono">{a.node.address}</span></div>
            {/if}
            {#if a}
              {#if a.by}<p class="text-xs text-fg-muted">{admissionWhat(a)} · {a.by}</p>{/if}
              {#if a.detail}<p class="text-sm leading-6 text-fg-muted">{a.detail}</p>{/if}
            {/if}
            <div class="flex flex-wrap items-center gap-2">
              {#if a && a.invited && !a.asked && admissionPending(a)}
                <Button variant="primary" size="sm" icon={Check} loading={acting.has(c.hash)} onclick={() => onask(c.hash)}>Accept invitation</Button>
                <Button size="sm" icon={X} loading={acting.has(a.id)} onclick={() => onrefuse(a)}>Decline</Button>
              {:else if a && a.asked && admissionPending(a)}
                <Button size="sm" icon={X} loading={acting.has(a.id)} onclick={() => onrefuse(a)}>Cancel request</Button>
              {:else if a && a.state === AdmissionState.ADMITTED}
                <span class="text-sm text-fg-muted">Connecting to the mesh…</span>
              {:else}
                <Button variant="primary" size="sm" icon={Send} loading={acting.has(c.hash)} disabled={!c.mesh && !a?.node?.address} onclick={() => onask(c.hash)}>{a && admissionSettled(a) ? 'Request again' : 'Request to join'}</Button>
                {#if a && admissionSettled(a)}<Button size="sm" icon={Trash2} loading={acting.has(a.id)} onclick={() => ondismiss(a)}>Dismiss</Button>{/if}
              {/if}
            </div>
          </div>
        </Card>
      {/each}
    </div>
  {/if}
</Section>
