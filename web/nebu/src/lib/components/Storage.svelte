<script lang="ts">
  import type { HostProfile } from '$proto/host_pb';
  import { homePath } from '$lib/state.svelte';
  import { HardDrive } from '@lucide/svelte';
  import Meters, { type Meter } from './ui/Meters.svelte';

  // One meter per filesystem nebu writes to, laid out like a device: the bar shows what the disk holds, the
  // fold names its filesystem and the paths of nebu's on it
  let { host }: { host: HostProfile } = $props();

  const meters = $derived(
    host.storage.map((s): Meter => {
      const used = s.totalBytes > s.freeBytes ? s.totalBytes - s.freeBytes : 0n;
      return {
        id: s.path,
        icon: HardDrive,
        name: s.path,
        total: s.totalBytes,
        items: used > 0n ? [{ size: used, tone: 'neutral' }] : [],
        units: 'decimal',
        facts: { path: s.path, filesystem: s.filesystem, holds: s.uses.map(homePath).join('\n') }
      };
    })
  );
</script>

<Meters {meters} name="Filesystem" bar="Space" empty="No filesystems probed" unsized="Size not probed" />
