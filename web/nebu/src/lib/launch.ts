import { api } from './api';
import { live, instanceLive, modelKey } from './state.svelte';
import { fail, ok } from './toast.svelte';
import type { StoredModel } from '$proto/store_pb';

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
}

// Reports whether a slot has something alive in it
export function slotOccupied(slotId: string | undefined): boolean {
  if (!slotId) return false;
  const s = live.slots.get(slotId);
  return !!s?.instanceId && instanceLive(live.instances.get(s.instanceId));
}

// Runs a model, swapping when its slot is occupied, and returns the task id
export async function launch(spec: RunSpec, drainFirst = false): Promise<string | undefined> {
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
    return undefined;
  }
}

// Runs a stored model by key into a slot
export async function launchKey(key: string, slotId: string): Promise<string | undefined> {
  const m = live.models.get(key);
  if (!m) {
    fail(new Error(key), 'Unknown model');
    return undefined;
  }
  return launch({ sourceId: m.sourceId, repo: m.repo, group: m.group, slotId });
}

export function specOf(m: StoredModel, slotId = ''): RunSpec {
  return { sourceId: m.sourceId, repo: m.repo, group: m.group, slotId };
}

export { modelKey };
