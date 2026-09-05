import { PoolKind } from '$proto/host_pb';
import type { Instance } from '$proto/instance_pb';
import { bytes } from './format';

// Device memory as the runtime reported it, else what the plan placed on the devices, empty when neither is known
export function instanceMemory(i: Instance): string {
  const m = i.measurements.find((x) => x.key === 'device.used') ?? i.measurements.find((x) => x.key.endsWith('.used'));
  if (m) return bytes(m.bytes);
  const planned = i.plan?.pools.filter((p) => p.kind === PoolKind.DEVICE || p.kind === PoolKind.UNIFIED).reduce((a, p) => a + p.usedBytes, 0n) ?? 0n;
  return planned ? '≈ ' + bytes(planned) : '';
}
