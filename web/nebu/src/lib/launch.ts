import { api } from './api';
import { live, instanceLive } from './state.svelte';
import { confirm } from './confirm.svelte';
import { droppedModel } from './dnd.svelte';
import { fail, ok } from './toast.svelte';

interface RunSpec {
  sourceId: string;
  repo: string;
  group: string;
  runtimeId?: string;
  installId?: string;
  name?: string;
  params?: Record<string, string>;
  slotId?: string;
  profileId?: string;
  // Launches even when the plan says no, redoing any prepare step
  force?: boolean;
}

// Reports whether a slot has something alive in it
export function slotOccupied(slotId: string | undefined): boolean {
  if (!slotId) return false;
  const s = live.slots.get(slotId);
  return !!s?.instanceId && instanceLive(live.instances.get(s.instanceId));
}

// Runs a model, swapping when its slot is occupied, and returns the task id
export async function launch(spec: RunSpec, drainFirst = false, refused?: (err: unknown) => void): Promise<string | undefined> {
  const run = { ...spec, params: spec.params ?? {} };
  const swap = slotOccupied(spec.slotId);
  try {
    const task = swap ? (await api.slots.swap({ slotId: spec.slotId!, run, drainFirst })).task : (await api.instances.run(run)).task;
    const id = task?.id ?? '';
    const target = spec.slotId ? `into ${live.slots.get(spec.slotId)?.name ?? 'slot'}` : '';
    ok(swap ? `Swapping ${spec.repo} ${target}` : `Starting ${spec.repo} ${target}`, spec.group, id ? { href: `/tasks?id=${id}`, label: 'Follow the task' } : undefined);
    return id;
  } catch (err) {
    fail(err, swap ? 'Swap refused' : 'Run refused');
    refused?.(err);
    return undefined;
  }
}

// Runs the dropped model in a slot, asking first when that swaps out its occupant
export async function dropModelOnSlot(ev: DragEvent, slotId: string) {
  ev.preventDefault();
  const key = droppedModel(ev);
  const slot = live.slots.get(slotId);
  if (!key || !slot) return;
  const m = live.models.get(key);
  if (!m) {
    fail(new Error(key), 'Unknown model');
    return;
  }
  if (slotOccupied(slotId)) {
    const yes = await confirm({
      title: `Swap ${slot.name}?`,
      message: `${slot.name} is serving ${slot.request?.repo ?? 'a model'}. It switches to ${m.repo} ${m.group} and the old instance drains and stops. The public name keeps answering throughout.`,
      action: 'Swap'
    });
    if (!yes) return;
  }
  await launch({ sourceId: m.sourceId, repo: m.repo, group: m.group, slotId });
}
