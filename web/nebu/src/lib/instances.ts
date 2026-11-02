import { Placement } from '$proto/estimate_pb';
import type { Instance } from '$proto/instance_pb';
import { bytes } from './format';
import { instanceUsage } from './state.svelte';

// Device memory the host attributes to the instance's processes now, else what it measured when the instance became ready
export function instanceMemory(i: Instance): string {
  const now = instanceUsage(i.id);
  if (now > 0n) return bytes(now);
  const m = i.measurements.find((x) => x.key === 'device.used');
  return m ? bytes(m.bytes) : '';
}

export const placements: { id: string; label: string; value: Placement }[] = [
  { id: 'auto', label: 'GPU, then RAM', value: Placement.UNSPECIFIED },
  { id: 'device', label: 'GPU only', value: Placement.DEVICE },
  { id: 'host', label: 'RAM only', value: Placement.HOST }
];

export function placementId(p: Placement | undefined): string {
  return placements.find((x) => x.value === p)?.id ?? 'auto';
}

export function placementOf(id: string): Placement {
  return placements.find((x) => x.id === id)?.value ?? Placement.UNSPECIFIED;
}

export function placementLabel(p: Placement | undefined): string {
  return placements.find((x) => x.value === p)?.label ?? placements[0].label;
}
