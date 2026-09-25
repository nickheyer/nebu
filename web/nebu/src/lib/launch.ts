import { api } from './api';
import { live, instanceLive, formationLive, startedTask } from './state.svelte';
import { fail } from './toast.svelte';
import { Shape, PlanProfile } from '$proto/estimate_pb';

export interface RunSpec {
  sourceId: string;
  repo: string;
  group: string;
  runtimeId?: string;
  installId?: string;
  name?: string;
  params?: Record<string, string>;
  slotId?: string;
  // Override the plan refusal and repeat preparation.
  force?: boolean;
  // Mesh nodes the run may use, which makes it a formation, with the shape and profile the planner takes
  span?: string[];
  shape?: Shape;
  profile?: PlanProfile;
}

// Whether a run spans the mesh: it names nodes or a shape
export function meshRun(spec: RunSpec): boolean {
  return (spec.span?.length ?? 0) > 0 || (spec.shape ?? Shape.UNSPECIFIED) !== Shape.UNSPECIFIED;
}

export function slotOccupied(slotId: string | undefined): boolean {
  if (!slotId) return false;
  const s = live.slots.get(slotId);
  if (s?.formationId && formationLive(live.formations.get(s.formationId))) return true;
  return !!s?.instanceId && instanceLive(live.instances.get(s.instanceId));
}

// Swap if the slot is occupied. Return the task ID.
export async function launch(spec: RunSpec, drainFirst = false, refused?: (err: unknown) => void): Promise<string | undefined> {
  const run = { ...spec, params: spec.params ?? {}, span: spec.span ?? [], shape: spec.shape ?? Shape.UNSPECIFIED, profile: spec.profile ?? PlanProfile.UNSPECIFIED };
  const swap = slotOccupied(spec.slotId);
  try {
    const task = swap ? (await api.slots.swap({ slotId: spec.slotId!, run, drainFirst })).task : meshRun(spec) ? (await api.mesh.runFormation({ run })).task : (await api.instances.run(run)).task;
    const id = task?.id ?? '';
    const target = spec.slotId ? ` in ${live.slots.get(spec.slotId)?.name ?? 'slot'}` : '';
    const what = `${spec.repo.split('/').pop()}${target}`;
    startedTask(`${swap ? 'Swapping' : 'Starting'} ${what}`, `${swap ? 'Swapped' : 'Started'} ${what}`, spec.group, task);
    return id;
  } catch (err) {
    fail(err, swap ? 'Swap refused' : 'Run refused');
    refused?.(err);
    return undefined;
  }
}
