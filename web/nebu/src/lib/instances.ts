import { PoolKind } from '$proto/host_pb';
import { Placement } from '$proto/estimate_pb';
import type { Instance } from '$proto/instance_pb';
import { bytes } from './format';

// Device memory as the runtime reported it, else what the plan placed on the devices, empty when neither is known
export function instanceMemory(i: Instance): string {
  const m = i.measurements.find((x) => x.key === 'device.used') ?? i.measurements.find((x) => x.key.endsWith('.used'));
  if (m) return bytes(m.bytes);
  const planned = i.plan?.pools.filter((p) => p.kind === PoolKind.DEVICE || p.kind === PoolKind.UNIFIED).reduce((a, p) => a + p.usedBytes, 0n) ?? 0n;
  return planned ? '≈ ' + bytes(planned) : '';
}

// The three places a slot keeps a model, as the form offers them
export const placements: { id: string; label: string; value: Placement }[] = [
  { id: 'auto', label: 'GPU, then RAM', value: Placement.UNSPECIFIED },
  { id: 'device', label: 'GPU only', value: Placement.DEVICE },
  { id: 'host', label: 'RAM only', value: Placement.HOST }
];

// The form's choice for a placement
export function placementId(p: Placement | undefined): string {
  return placements.find((x) => x.value === p)?.id ?? 'auto';
}

// The placement the daemon takes for a form's choice
export function placementOf(id: string): Placement {
  return placements.find((x) => x.id === id)?.value ?? Placement.UNSPECIFIED;
}

// A placement in the words the form uses
export function placementLabel(p: Placement | undefined): string {
  return placements.find((x) => x.value === p)?.label ?? placements[0].label;
}
