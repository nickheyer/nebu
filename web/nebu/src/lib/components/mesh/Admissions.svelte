<script lang="ts">
  import { Check, X, RotateCcw, Trash2 } from '@lucide/svelte';
  import { clock } from '$lib/state.svelte';
  import { admissionLabel, admissionTone, admissionWhat, admissionPending } from '$lib/mesh';
  import { ago, when } from '$lib/format';
  import { AdmissionState, type Admission, type NearbyNode } from '$proto/mesh_pb';
  import Section from '$lib/components/ui/Section.svelte';
  import State from '$lib/components/ui/State.svelte';
  import Button from '$lib/components/ui/Button.svelte';

  let {
    waiting,
    pendingCount,
    acting,
    onadmit,
    onrefuse,
    oninvite,
    ondismiss
  }: {
    waiting: Admission[];
    pendingCount: number;
    acting: Set<string>;
    onadmit: (a: Admission) => void;
    onrefuse: (a: Admission) => void;
    oninvite: (n: NearbyNode | undefined) => void;
    ondismiss: (a: Admission) => void;
  } = $props();
</script>

<Section title="Requests and invitations" count={pendingCount || undefined}>
  <div class="tbl-wrap">
    <table class="tbl">
      <thead><tr><th>Node</th><th>Type</th><th>Status</th><th>Updated</th><th class="actions"></th></tr></thead>
      <tbody>
        {#each waiting as a (a.id)}
          <tr>
            <td class="text-fg">
              {a.node?.name || a.node?.id.slice(0, 12) || '–'}
              {#if a.node?.address}<div class="font-mono text-xs text-fg-muted">{a.node.address}</div>{/if}
            </td>
            <td class="text-fg-muted">{admissionWhat(a)}</td>
            <td>
              <State tone={admissionTone(a.state)} label={admissionLabel(a)} pulse={a.state === AdmissionState.ADMITTED} />
              {#if a.detail}<p class="mt-1 max-w-sm text-xs leading-5 text-fg-muted">{a.detail}</p>{/if}
            </td>
            <td class="text-xs text-fg-muted" title={[when(a.updatedAt), a.by].filter(Boolean).join(' · ')}>{ago(a.updatedAt, clock.now)}</td>
            <td class="actions">
              <div class="flex justify-end gap-2">
                {#if admissionPending(a) && a.asked}
                  <Button variant="primary" size="sm" icon={Check} loading={acting.has(a.id)} onclick={() => onadmit(a)}>Approve</Button>
                  <Button size="sm" icon={X} loading={acting.has(a.id)} onclick={() => onrefuse(a)}>Deny</Button>
                {:else if admissionPending(a)}
                  <Button size="sm" icon={X} loading={acting.has(a.id)} onclick={() => onrefuse(a)}>Cancel invitation</Button>
                {:else if a.state === AdmissionState.ADMITTED}
                  <span class="self-center text-xs text-fg-muted">Joining</span>
                {:else}
                  {#if a.state === AdmissionState.FAILED || a.state === AdmissionState.EXPIRED}
                    <Button size="sm" icon={RotateCcw} loading={acting.has(a.id)} onclick={() => (a.asked ? onadmit(a) : oninvite(a.node))}>Try again</Button>
                  {/if}
                  <Button size="sm" icon={Trash2} loading={acting.has(a.id)} onclick={() => ondismiss(a)}>Dismiss</Button>
                {/if}
              </div>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
</Section>
