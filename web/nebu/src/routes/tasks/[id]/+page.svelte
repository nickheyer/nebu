<script lang="ts">
  import { page } from '$app/state';
  import { live, slotName } from '$lib/state.svelte';
  import { when } from '$lib/format';
  import { ExternalLink } from '@lucide/svelte';
  import PageHeader from '$lib/components/ui/PageHeader.svelte';
  import TaskLog from '$lib/components/TaskLog.svelte';

  const id = $derived(page.params.id ?? '');
  const task = $derived(live.tasks.get(id));
  const related = $derived.by(() => {
    const l = task?.labels ?? {};
    const out: { label: string; href: string }[] = [];
    if (l.instance) out.push({ label: 'Instance', href: `/instances/${l.instance}` });
    if (l.slot) out.push({ label: `Slot ${slotName(l.slot)}`, href: `/slots/${l.slot}` });
    if (l.runtime) out.push({ label: 'Runtime', href: `/runtimes/${l.runtime}` });
    if (l.repo) out.push({ label: 'Library', href: `/store` });
    return out;
  });
</script>

<PageHeader title={task?.title ?? 'Task'} back={{ href: '/tasks', label: 'Tasks' }}>
  {#snippet meta()}
    {#if task}<span>{when(task.createdAt)}</span>{/if}
    {#each related as r (r.href)}
      <a href={r.href} class="link inline-flex items-center gap-1"><ExternalLink size={12} /> {r.label}</a>
    {/each}
  {/snippet}
</PageHeader>

{#if id}
  {#key id}<TaskLog {id} height="h-[calc(100vh-18rem)]" />{/key}
{/if}
