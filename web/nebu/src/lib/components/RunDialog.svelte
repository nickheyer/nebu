<script lang="ts">
  import { api, message } from '$lib/api';
  import { live } from '$lib/state.svelte';
  import { parsePairs } from '$lib/format';
  import type { StoredModel } from '$proto/store_pb';
  import Modal from './Modal.svelte';
  import TaskLog from './TaskLog.svelte';

  let { open = $bindable(false), model, slotId = '' }: { open?: boolean; model: StoredModel | null; slotId?: string } = $props();
  let runtimeId = $state('');
  let installId = $state('');
  let name = $state('');
  let slot = $state('');
  let params = $state('');
  let error = $state('');
  let taskId = $state('');
  let runtimes = $state<{ id: string; formats: string[]; compatible: boolean }[]>([]);

  $effect(() => {
    if (open) {
      slot = slotId;
      taskId = '';
      error = '';
      api.runtimes.listRuntimes({}).then((r) => {
        runtimes = r.runtimes.map((s) => ({ id: s.manifest?.id ?? '', formats: s.manifest?.formats ?? [], compatible: s.compatible }));
        const fit = runtimes.find((rt) => rt.compatible && rt.formats.includes(model?.formatId ?? ''));
        if (!runtimeId && fit) runtimeId = fit.id;
      });
    }
  });

  const installs = $derived([...live.installs.values()].filter((i) => i.runtimeId === runtimeId));
  const occupied = $derived(() => {
    const s = live.slots.get(slot);
    return !!s && !!s.instanceId;
  });

  async function submit() {
    if (!model) return;
    error = '';
    const run = { sourceId: model.sourceId, repo: model.repo, group: model.group, runtimeId, installId, name, params: parsePairs(params), slotId: slot };
    try {
      if (slot && occupied()) {
        const resp = await api.slots.swap({ slotId: slot, run });
        taskId = resp.task?.id ?? '';
      } else {
        const resp = await api.instances.run(run);
        taskId = resp.task?.id ?? '';
      }
    } catch (err) {
      error = message(err);
    }
  }
</script>

<Modal bind:open title={slot && occupied() ? 'Swap ' + (model?.repo ?? '') : 'Run ' + (model?.repo ?? '')}>
  {#if taskId}
    <TaskLog id={taskId} />
  {:else}
    <div class="grid grid-cols-2 gap-3">
      <div>
        <span class="label">Weight group</span>
        <div class="mono">{model?.group}</div>
      </div>
      <div>
        <label class="label" for="run-slot">Slot</label>
        <select id="run-slot" class="input" bind:value={slot}>
          <option value="">none, public name below</option>
          {#each [...live.slots.values()] as s (s.id)}
            <option value={s.id}>{s.name} {s.instanceId ? '(occupied, will swap)' : '(empty)'}</option>
          {/each}
        </select>
      </div>
      <div>
        <label class="label" for="run-runtime">Runtime</label>
        <select id="run-runtime" class="input" bind:value={runtimeId}>
          <option value="">slot default or first compatible</option>
          {#each runtimes as rt (rt.id)}
            <option value={rt.id}>{rt.id}{rt.compatible ? '' : ' (incompatible)'}</option>
          {/each}
        </select>
      </div>
      <div>
        <label class="label" for="run-install">Install</label>
        <select id="run-install" class="input" bind:value={installId}>
          <option value="">newest</option>
          {#each installs as i (i.id)}
            <option value={i.id}>{i.id} {i.version}</option>
          {/each}
        </select>
      </div>
      {#if !slot}
        <div class="col-span-2">
          <label class="label" for="run-name">Public name</label>
          <input id="run-name" class="input" bind:value={name} placeholder="repo:group by default" />
        </div>
      {/if}
      <div class="col-span-2">
        <label class="label" for="run-params">Params, one name=value per line</label>
        <textarea id="run-params" class="input h-20" bind:value={params} placeholder="n_ctx=8192"></textarea>
      </div>
    </div>
    {#if error}<div class="mt-2 text-sm text-red-300">{error}</div>{/if}
    <div class="mt-4 flex justify-end">
      <button class="btn btn-primary" onclick={submit}>{slot && occupied() ? 'swap' : 'run'}</button>
    </div>
  {/if}
</Modal>
