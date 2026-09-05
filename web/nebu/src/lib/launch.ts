import { api } from './api';
import { live, instanceLive } from './state.svelte';
import { fail, ok } from './toast.svelte';

export interface RunSpec {
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
    ok(swap ? `Swapping ${spec.repo} ${target}` : `Starting ${spec.repo} ${target}`, spec.group, id ? { href: `/tasks?id=${id}`, label: 'Task' } : undefined);
    return id;
  } catch (err) {
    fail(err, swap ? 'Swap refused' : 'Run refused');
    refused?.(err);
    return undefined;
  }
}
