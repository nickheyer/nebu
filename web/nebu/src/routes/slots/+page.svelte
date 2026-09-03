<script lang="ts">
  import { api, message } from '$lib/api';
  import { live } from '$lib/state.svelte';
  import { enumName, human, parseBytes, parsePairs } from '$lib/format';
  import SlotCard from '$lib/components/SlotCard.svelte';
  import TaskLog from '$lib/components/TaskLog.svelte';
  import InstanceLog from '$lib/components/InstanceLog.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import RunDialog from '$lib/components/RunDialog.svelte';
  import type { Slot } from '$proto/slot_pb';
  import type { StoredModel } from '$proto/store_pb';
  import { DeviceKind } from '$proto/host_pb';
  import { InstanceState } from '$proto/instance_pb';
  import { SlotState } from '$proto/slot_pb';

  let createOpen = $state(false);
  let name = $state('');
  let description = $state('');
  let memory = $state('');
  let runtimeId = $state('');
  let params = $state('');
  let devices = $state<string[]>([]);
  let error = $state('');
  let taskId = $state('');
  let selected = $state<Slot | null>(null);
  let runOpen = $state(false);
  let runModel = $state<StoredModel | null>(null);

  const slots = $derived([...live.slots.values()].sort((a, b) => a.name.localeCompare(b.name)));
  const models = $derived([...live.models.values()].sort((a, b) => (a.repo + a.group).localeCompare(b.repo + b.group)));
  const gpus = $derived((live.host?.devices ?? []).filter((d) => d.kind !== DeviceKind.CPU));
  const occupant = $derived(selected?.instanceId ? live.instances.get(selected.instanceId) : undefined);

  async function create() {
    error = '';
    try {
      await api.slots.createSlot({ name, description, deviceIds: devices, memoryBytes: parseBytes(memory), runtimeId, params: parsePairs(params) });
      createOpen = false;
      name = description = memory = runtimeId = params = '';
      devices = [];
    } catch (err) {
      error = message(err);
    }
  }

  async function dropModel(slotId: string, key: string) {
    const model = live.models.get(key);
    if (!model) return;
    error = '';
    try {
      const resp = await api.slots.swap({ slotId, run: { sourceId: model.sourceId, repo: model.repo, group: model.group, slotId } });
      taskId = resp.task?.id ?? '';
    } catch (err) {
      error = message(err);
    }
  }

  async function remove(slot: Slot) {
    if (!confirm(`delete slot ${slot.name}${slot.instanceId ? ' and stop what it serves' : ''}?`)) return;
    error = '';
    try {
      await api.slots.deleteSlot({ id: slot.id, force: true });
      if (selected?.id === slot.id) selected = null;
    } catch (err) {
      error = message(err);
    }
  }

  function drag(ev: DragEvent, m: StoredModel) {
    ev.dataTransfer?.setData('text/nebu-model', `${m.sourceId}/${m.repo}/${m.group}`);
  }

  function toggle(id: string) {
    devices = devices.includes(id) ? devices.filter((d) => d !== id) : [...devices, id];
  }
</script>

<div class="space-y-4">
  <div class="flex items-center gap-3">
    <h1 class="h1">Slots</h1>
    <button class="btn btn-primary ml-auto" onclick={() => (createOpen = true)}>new slot</button>
  </div>
  {#if error}<div class="text-sm text-red-300">{error}</div>{/if}

  <div class="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
    <div class="space-y-3">
      <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
        {#each slots as slot (slot.id)}
          <SlotCard {slot} onDropModel={dropModel} onSelect={(s) => (selected = s)} />
        {:else}
          <div class="card muted">no slots, create one to reserve devices under a stable public name</div>
        {/each}
      </div>
      {#if taskId}<div class="card"><TaskLog id={taskId} /></div>{/if}
      {#if selected}
        {@const s = live.slots.get(selected.id) ?? selected}
        <div class="card space-y-2">
          <div class="flex items-center gap-2">
            <h2 class="h2">{s.name}</h2>
            <span class="muted mono text-xs">{s.id}</span>
            <div class="ml-auto flex gap-2">
              <button class="btn" onclick={() => { runModel = models[0] ?? null; runOpen = true; }} disabled={!models.length}>run or swap</button>
              <button class="btn btn-danger" onclick={() => remove(s)}>delete</button>
            </div>
          </div>
          <div class="muted text-sm">{s.description}</div>
          <div class="text-sm">state {enumName(SlotState, s.state)}, budget {s.memoryBytes ? human(s.memoryBytes) : 'whole devices'}, runtime {s.runtimeId || 'default'}</div>
          {#if occupant}
            <div class="text-sm">
              instance <span class="mono">{occupant.id}</span> {enumName(InstanceState, occupant.state)} pid {occupant.pid} at {occupant.endpoint}
            </div>
            <div class="muted text-xs">{occupant.command.join(' ')}</div>
            <InstanceLog id={occupant.id} />
          {/if}
        </div>
      {/if}
    </div>
    <div class="card">
      <h2 class="h2 mb-2">Stored models</h2>
      <p class="muted mb-2 text-xs">drag onto a slot to run it there, or swap what the slot serves</p>
      <ul class="divide-y divide-zinc-800">
        {#each models as m (m.sourceId + m.repo + m.group)}
          <li draggable="true" ondragstart={(e) => drag(e, m)} class="flex cursor-grab items-center gap-2 py-1.5 text-sm">
            <span class="muted">::</span>
            <span class="mono">{m.repo}</span>
            <span class="muted">{m.group}</span>
            <span class="muted ml-auto">{human(m.bytes)}</span>
          </li>
        {:else}
          <li class="muted py-2 text-sm">nothing stored yet</li>
        {/each}
      </ul>
    </div>
  </div>

  <Modal bind:open={createOpen} title="New slot">
    <div class="grid grid-cols-2 gap-3">
      <div><label class="label" for="slot-name">Name, becomes the public model name</label><input id="slot-name" class="input" bind:value={name} placeholder="main" /></div>
      <div><label class="label" for="slot-memory">Device memory budget</label><input id="slot-memory" class="input" bind:value={memory} placeholder="8GiB, empty for whole devices" /></div>
      <div class="col-span-2">
        <span class="label">Devices, all when none picked</span>
        <div class="flex flex-wrap gap-2">
          {#each gpus as d (d.id)}
            <button type="button" class="btn {devices.includes(d.id) ? 'btn-primary' : ''}" onclick={() => toggle(d.id)}>{d.name} <span class="muted text-xs">{human(d.memoryTotalBytes)}</span></button>
          {:else}
            <span class="muted text-sm">no devices probed</span>
          {/each}
        </div>
      </div>
      <div><label class="label" for="slot-runtime">Default runtime</label><input id="slot-runtime" class="input" bind:value={runtimeId} placeholder="llamacpp" /></div>
      <div><label class="label" for="slot-desc">Description</label><input id="slot-desc" class="input" bind:value={description} /></div>
      <div class="col-span-2"><label class="label" for="slot-params">Default params, one name=value per line</label><textarea id="slot-params" class="input h-16" bind:value={params}></textarea></div>
    </div>
    <div class="mt-4 flex justify-end"><button class="btn btn-primary" onclick={create} disabled={!name}>create</button></div>
  </Modal>
  <RunDialog bind:open={runOpen} model={runModel} slotId={selected?.id ?? ''} />
</div>
