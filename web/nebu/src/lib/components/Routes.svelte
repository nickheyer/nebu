<script lang="ts">
  import { api } from '$lib/api';
  import { live, cached, slotName, groupLabel, instanceLive, orderedSlots } from '$lib/state.svelte';
  import { policyText, policyFields, policyFrom, profileText, profileFrom } from '$lib/gateway';
  import { byName, count, tail } from '$lib/format';
  import { fail, ok } from '$lib/toast.svelte';
  import { confirm } from '$lib/confirm.svelte';
  import { RouteState } from '$proto/gateway_pb';
  import { InstanceState } from '$proto/instance_pb';
  import { SlotState } from '$proto/slot_pb';
  import { Plus, Trash2 } from '@lucide/svelte';
  import Section from './ui/Section.svelte';
  import Button from './ui/Button.svelte';
  import IconButton from './ui/IconButton.svelte';
  import Select from './ui/Select.svelte';
  import State from './ui/State.svelte';
  import Empty from './ui/Empty.svelte';
  import Field from './ui/Field.svelte';
  import Dialog from './ui/Dialog.svelte';
  import PolicyForm from './PolicyForm.svelte';
  import ProfileForm from './ProfileForm.svelte';
  import TextInput from './ui/TextInput.svelte';

  let open = $state(false);
  let aliasName = $state('');
  // slot:<id> or instance:<id>
  let target = $state('');
  let policy = $state(policyFields(undefined));
  let systemMessages = $state('auto');
  let adding = $state(false);

  const routes = $derived([...live.routes.values()].sort(byName((r) => r.name)));
  // Slots take aliases in any state. Instances outside slots must be ready.
  const slots = $derived(orderedSlots());
  const standalone = $derived([...live.instances.values()].filter((i) => i.state === InstanceState.READY && !i.slotId));
  const targets = $derived([
    ...slots.map((s) => ({ value: `slot:${s.id}`, label: s.name, detail: s.request ? `slot · ${tail(s.request.repo)} ${groupLabel(s.request)}` : `slot · ${s.state === SlotState.EMPTY ? 'empty' : 'waiting'}` })),
    ...standalone.map((i) => ({ value: `instance:${i.id}`, label: i.name, detail: `instance · ${tail(i.repo)} ${groupLabel(i)}` }))
  ]);
  const anyTarget = $derived(slots.length > 0 || [...live.instances.values()].some(instanceLive));
  const nameTaken = $derived(live.routes.has(aliasName.trim()) || slots.some((s) => s.name === aliasName.trim() || s.aliases.some((a) => a.name === aliasName.trim())));
  const badName = $derived(/[\s/]/.test(aliasName));
  const defaults = $derived(cached.gateway?.policy);
  const canAdd = $derived(!!aliasName.trim() && !nameTaken && !badName && !!target);

  function openForm() {
    aliasName = '';
    target = targets[0]?.value ?? '';
    policy = policyFields(undefined);
    systemMessages = 'auto';
    open = true;
  }

  async function addAlias() {
    adding = true;
    const [kind, id] = target.split(':', 2);
    try {
      await api.gateway.setRoute({ name: aliasName.trim(), slotId: kind === 'slot' ? id : '', instanceId: kind === 'instance' ? id : '', policy: policyFrom(policy), profile: profileFrom(systemMessages) });
      ok(`Added ${aliasName.trim()}`);
      open = false;
    } catch (err) {
      fail(err, 'Alias refused');
    } finally {
      adding = false;
    }
  }

  // A slot's own name goes with the slot. Its aliases and instance aliases can be removed here.
  function removable(r: { name: string; slotId: string }): boolean {
    return !r.slotId || slotName(r.slotId) !== r.name;
  }

  async function remove(name: string) {
    const yes = await confirm({ title: `Remove ${name}?`, message: `Requests for "${name}" will get 404.`, action: 'Remove', tone: 'bad' });
    if (!yes) return;
    try {
      await api.gateway.deleteRoute({ name });
      ok(`Removed ${name}`);
    } catch (err) {
      fail(err, 'Remove failed');
    }
  }
</script>

{#if anyTarget}
<Section title="Aliases" count={routes.length || undefined}>
  {#snippet actions()}
    <Button size="sm" icon={Plus} disabled={!targets.length} onclick={openForm}>Add alias</Button>
  {/snippet}
  {#if routes.length === 0}
    <Empty compact title="No aliases yet" />
  {:else}
    <div class="tbl-wrap">
      <table class="tbl">
        <thead><tr><th>Name</th><th>State</th><th>Serves</th><th>Endpoint</th><th class="num">Requests</th><th>Limits</th><th>Shaping</th><th></th></tr></thead>
        <tbody>
          {#each routes as r (r.name)}
            {@const inst = r.instanceId ? live.instances.get(r.instanceId) : undefined}
            <tr>
              <td class="font-mono text-xs text-fg">{r.name}</td>
              <td><State values={RouteState} value={r.state} /></td>
              <td class="text-fg-muted">
                {#if r.slotId}<a class="link" href="/slots/{r.slotId}">Slot {slotName(r.slotId)}</a>{#if removable(r)}<span class="text-fg-faint">{' · alias'}</span>{/if}{#if inst}<span class="text-fg-faint">{' · '}{tail(inst.repo)} {groupLabel(inst)}</span>{/if}{:else if inst}<a class="link" href="/instances/{inst.id}">{inst.name}</a><span class="text-fg-faint">{' · '}{tail(inst.repo)} {groupLabel(inst)}</span>{:else if r.state === RouteState.PENDING}Waiting for a model{:else}–{/if}
                {#if r.served && r.served !== r.name}<div class="font-mono text-xs text-fg-faint">runtime name {r.served}</div>{/if}
              </td>
              <td class="font-mono text-xs text-fg-muted">{r.endpoint || '–'}</td>
              <td class="num">{count(r.requests)}{#if r.inFlight}<span class="text-fg-faint">{' · '}{r.inFlight} live</span>{/if}</td>
              <td class="text-xs text-fg-muted">{policyText(r.policy, defaults)}</td>
              <td class="text-xs text-fg-muted">{profileText(r.profile, inst?.template)}</td>
              <td class="actions"><span>{#if removable(r)}<IconButton size="xs" icon={Trash2} label="Remove" onclick={() => remove(r.name)} />{/if}</span></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</Section>
{/if}

<Dialog bind:open title="Add alias" description="Another model name clients can send. Slot aliases stay through swaps and restarts." size="lg">
  <form
    class="flex flex-col gap-5"
    onsubmit={(e) => {
      e.preventDefault();
      if (canAdd && !adding) addAlias();
    }}
  >
    <div class="grid grid-cols-1 gap-x-5 gap-y-4 sm:grid-cols-2">
      <Field label="Name" for="alias-name" required description="Clients send this as the model name." error={nameTaken ? 'That name is taken' : badName ? 'No spaces or slashes' : undefined}>
        <TextInput id="alias-name" mono bind:value={aliasName} empty="gpt-4" invalid={nameTaken || badName} />
      </Field>
      <Field label="Serves" for="alias-target" required>
        <Select id="alias-target" bind:value={target} items={targets} />
      </Field>
    </div>
    <div class="flex flex-col gap-3">
      <h3 class="caps text-fg-faint">Limits</h3>
      <PolicyForm bind:fields={policy} defaults={defaults} idPrefix="alias" />
    </div>
    <div class="flex flex-col gap-3">
      <h3 class="caps text-fg-faint">Shaping</h3>
      <ProfileForm bind:value={systemMessages} idPrefix="alias" />
    </div>
    <button type="submit" class="hidden" aria-hidden="true" tabindex="-1"></button>
  </form>
  {#snippet footer()}
    <span class="ml-auto flex gap-2">
      <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
      <Button variant="primary" icon={Plus} loading={adding} disabled={!canAdd} onclick={addAlias}>Add alias</Button>
    </span>
  {/snippet}
</Dialog>
