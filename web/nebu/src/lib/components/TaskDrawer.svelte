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
    if (l.instance) out.push({ label: 'instance', href: `/instances?id=${l.instance}` });
    if (l.slot) out.push({ label: 'slot', href: `/slots?id=${l.slot}` });
    if (l.build) out.push({ label: 'build', href: `/runtimes#builds` });
    if (l.repo) out.push({ label: 'model', href: `/store` });
    if (l.watch) out.push({ label: 'watch', href: `/monitor` });
    return out;
  });
</script>

<Drawer open={!!id} onOpenChange={(v: boolean) => { if (!v) id = ''; }} title={task?.title ?? 'Task'} subtitle={id} width="xl">
  {#snippet header()}
    {#if task}
      <div class="flex flex-wrap items-center gap-3 text-xs text-fg-muted">
        <span>created {when(task.createdAt)}</span>
        {#each related as r (r.href)}
          <a href={r.href} class="inline-flex items-center gap-1 text-accent hover:underline" onclick={() => (id = '')}><ExternalLink size={11} /> {r.label}</a>
        {/each}
      </div>
    {/if}
  {/snippet}
  <div class="px-5 py-4">
    {#if id}
      {#key id}<TaskLog {id} height="h-[calc(100vh-15rem)]" />{/key}
    {/if}
  </div>
</Drawer>
