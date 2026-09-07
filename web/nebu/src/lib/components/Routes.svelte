<script lang="ts">
  import { api } from '$lib/api';
  import { live, cached, slotName, groupLabel } from '$lib/state.svelte';
  import { policyText } from '$lib/gateway';
  import { byName, count, tail } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { RouteState } from '$proto/gateway_pb';
  import { InstanceState } from '$proto/instance_pb';
  import { Plus, Trash2 } from '@lucide/svelte';
  import Section from './ui/Section.svelte';
  import Button from './ui/Button.svelte';
  import IconButton from './ui/IconButton.svelte';
  import Select from './ui/Select.svelte';
  import State from './ui/State.svelte';
  import Empty from './ui/Empty.svelte';

  // Every name the gateway answers to, what stands behind it, and an alias form
  let aliasName = $state('');
  let aliasInstance = $state('');
  let adding = $state(false);

  const routes = $derived([...live.routes.values()].sort(byName((r) => r.name)));
  const ready = $derived([...live.instances.values()].filter((i) => i.state === InstanceState.READY));
  const nameTaken = $derived(live.routes.has(aliasName.trim()));
  const defaults = $derived(cached.gateway?.policy);

  async function addAlias() {
    adding = true;
    try {
      await api.gateway.setRoute({ name: aliasName.trim(), instanceId: aliasInstance });
      ok(`${aliasName.trim()} now answers`);
      aliasName = '';
    } catch (err) {
      fail(err, 'Alias refused');
    } finally {
      adding = false;
    }
  }

  async function remove(name: string) {
    const yes = await confirm({ title: `Remove ${name}?`, message: 'Clients using this name get 404.', action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.gateway.deleteRoute({ name });
      ok(`Removed ${name}`);
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }
</script>

<Section title="Model names" count={routes.length || undefined} meta="what clients send as the model">
  {#if routes.length === 0}
    <Empty compact title="Nothing answers yet" />
  {:else}
    <div class="overflow-x-auto">
      <table class="tbl">
        <thead><tr><th>Name</th><th>State</th><th>Serves</th><th>Endpoint</th><th class="num">Requests</th><th>Limits</th><th></th></tr></thead>
        <tbody>
          {#each routes as r (r.name)}
            {@const inst = r.instanceId ? live.instances.get(r.instanceId) : undefined}
            <tr>
              <td class="font-mono text-xs text-fg">{r.name}</td>
              <td><State values={RouteState} value={r.state} /></td>
              <td class="text-fg-muted">
                {#if r.slotId}slot <span class="font-mono text-xs">{slotName(r.slotId)}</span>{#if inst}<span class="text-fg-faint">{' · '}{tail(inst.repo)} {groupLabel(inst)}</span>{/if}{:else if inst}{inst.name}<span class="text-fg-faint">{' · '}{tail(inst.repo)} {groupLabel(inst)}</span>{:else if r.state === RouteState.PENDING}waiting{:else}–{/if}
                {#if r.served && r.served !== r.name}<div class="font-mono text-xs text-fg-faint">upstream {r.served}</div>{/if}
              </td>
              <td class="font-mono text-xs text-fg-muted">{r.endpoint || '–'}</td>
              <td class="num">{count(r.requests)}{#if r.inFlight}<span class="text-fg-faint">{' · '}{r.inFlight} live</span>{/if}</td>
              <td class="text-xs text-fg-muted">{policyText(r.policy, defaults)}</td>
              <td class="actions"><span>{#if !r.slotId}<IconButton size="sm" icon={Trash2} label="Remove" onclick={() => remove(r.name)} />{/if}</span></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
  {#if ready.length}
    <form
      class="flex flex-wrap items-center gap-2"
      onsubmit={(e) => {
        e.preventDefault();
        if (aliasName.trim() && !nameTaken && aliasInstance) addAlias();
      }}
    >
      <input class="input w-44 font-mono" bind:value={aliasName} placeholder="alias" aria-label="Alias" aria-invalid={nameTaken} autocomplete="off" spellcheck="false" />
      <span class="text-sm text-fg-faint">for</span>
      <Select class="w-64" bind:value={aliasInstance} label="Instance" placeholder="Instance" items={ready.map((i) => ({ value: i.id, label: i.name, detail: `${tail(i.repo)} ${groupLabel(i)}` }))} />
      <Button type="submit" icon={Plus} loading={adding} disabled={!aliasName.trim() || nameTaken || !aliasInstance}>Add alias</Button>
      {#if nameTaken}<span class="text-xs text-bad">That name is taken</span>{/if}
    </form>
  {/if}
</Section>
