<script lang="ts">
  import { DeviceKind, PoolKind, type HostProfile, type MemoryPool } from '$proto/host_pb';
  import { Cpu, MemoryStick, Microchip } from '@lucide/svelte';
  import Meters, { type Meter } from './ui/Meters.svelte';
  import type { SizeItem } from './ui/SizeBar.svelte';

  let { host }: { host: HostProfile } = $props();

  // Used memory as the host reports it
  function items(total: bigint, free: bigint): SizeItem[] {
    const used = total > free ? total - free : 0n;
    return used > 0n ? [{ size: used, tone: 'info' }] : [];
  }

  const meters = $derived.by((): Meter[] => {
    const owned = new Map<string, MemoryPool>();
    for (const p of host.pools) if (p.deviceId) owned.set(p.deviceId, p);
    const loose = host.pools.filter((p) => !p.deviceId);
    const system = loose.find((p) => p.kind === PoolKind.HOST);
    const unified = loose.find((p) => p.kind === PoolKind.UNIFIED);
    const users = new Set<string>();
    const poolOf = (d: HostProfile['devices'][number]): MemoryPool | undefined => {
      const own = owned.get(d.id);
      if (own) return own;
      if (d.kind === DeviceKind.CPU) return system ?? unified;
      return unified;
    };
    const out: Meter[] = host.devices.map((d) => {
      const pool = poolOf(d);
      if (pool) users.add(pool.id);
      const total = pool?.totalBytes ?? d.memoryTotalBytes;
      const free = pool?.freeBytes ?? d.memoryFreeBytes;
      return {
        id: d.id,
        icon: d.kind === DeviceKind.CPU ? Cpu : Microchip,
        name: d.name || d.id,
        total,
        items: items(total, free),
        facts: { id: d.id, vendor: d.vendor, ...d.facts }
      };
    });
    // Show memory pools even when no matching processor was found.
    for (const p of loose) {
      if (!users.has(p.id)) {
        out.push({ id: p.id, icon: MemoryStick, name: p.kind === PoolKind.UNIFIED ? 'Unified memory' : 'System memory', total: p.totalBytes, items: items(p.totalBytes, p.freeBytes), facts: {} });
      }
    }
    return out;
  });
</script>

<Meters {meters} name="Device" bar="Memory" empty="No devices probed" unsized="Memory not probed" />
