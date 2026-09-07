<script lang="ts">
  import { live } from '$lib/state.svelte';
  import { when } from '$lib/format';
  import { ExternalLink } from '@lucide/svelte';
  import Drawer from './ui/Drawer.svelte';
  import TaskLog from './TaskLog.svelte';

  let { id = $bindable('') }: { id?: string } = $props();
  const task = $derived(id ? live.tasks.get(id) : undefined);
  const related = $derived.by(() => {
    const l = task?.labels ?? {};
    const out: { label: string; href: string }[] = [];
    if (l.instance) out.push({ label: 'Instance', href: `/?instance=${l.instance}` });
    if (l.slot) out.push({ label: 'Slot', href: `/?slot=${l.slot}` });
    if (l.runtime) out.push({ label: 'Runtime', href: `/runtimes?id=${l.runtime}` });
    if (l.repo) out.push({ label: 'Library', href: `/store` });
    return out;
  });
</script>

<Drawer bind:id title={task?.title ?? 'Task'} subtitle={task ? when(task.createdAt) : id}>
  {#snippet header()}
    {#if related.length}
      <div class="flex flex-wrap items-center gap-3 text-sm">
        {#each related as r (r.href)}
          <a href={r.href} class="link inline-flex items-center gap-1" onclick={() => (id = '')}><ExternalLink size={12} /> {r.label}</a>
        {/each}
      </div>
    {/if}
  {/snippet}
  <div class="px-6 py-5">
    {#if id}
      {#key id}<TaskLog {id} height="h-[calc(100vh-16rem)]" />{/key}
    {/if}
  </div>
</Drawer>
