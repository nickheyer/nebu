<script lang="ts">
  import { live, liveInstances, hostName, updateSettings } from '$lib/state.svelte';
  import { Check, X, ArrowRight } from '@lucide/svelte';

  // Shows the state of this host until it serves something or is dismissed
  let label = $state('');
  let editing = $state(false);
  let saving = $state(false);

  const installs = $derived(live.installs.size);
  const models = $derived(live.models.size);
  const running = $derived(liveInstances().length);
  const complete = $derived(installs > 0 && models > 0 && running > 0);
  const shown = $derived(live.ready && !!live.settings && !live.settings.setupDismissed && !complete);

  const steps = $derived([
    { title: 'Runtime', value: installs ? `${installs} installed` : 'none installed', done: installs > 0, href: '/runtimes', action: 'Runtimes' },
    { title: 'Model', value: models ? `${models} stored` : 'none stored', done: models > 0, href: '/catalog', action: 'Catalog' },
    { title: 'Serving', value: running ? `${running} running` : 'nothing running', done: running > 0, href: models ? '/store' : '/catalog', action: models ? 'Store' : 'Catalog' }
  ]);

  function edit() {
    label = live.settings?.hostLabel ?? '';
    editing = true;
  }

  async function save() {
    if (saving) return;
    saving = true;
    const ok = await updateSettings({ hostLabel: label.trim() });
    saving = false;
    if (ok) editing = false;
  }

  function dismiss() {
    void updateSettings({ setupDismissed: true });
  }
</script>

{#if shown}
  <section class="relative overflow-hidden rounded-lg border border-line bg-surface" aria-label="Setup">
    <div class="absolute inset-y-0 left-0 w-1 bg-accent"></div>
    <div class="flex items-center gap-3 px-5 py-3">
      <h2 class="text-sm font-semibold text-fg">Setup</h2>
      <button class="ml-auto rounded-md p-1 text-fg-faint transition-colors hover:bg-raised hover:text-fg" aria-label="Dismiss" title="Dismiss" onclick={dismiss}><X size={15} /></button>
    </div>
    <div class="grid grid-cols-1 divide-y divide-line border-t border-line sm:grid-cols-2 sm:divide-y-0 sm:divide-x xl:grid-cols-4">
      <div class="flex min-h-[4.5rem] flex-col justify-center gap-1 px-5 py-3">
        <div class="text-[11px] font-semibold tracking-wider text-fg-faint uppercase">Name</div>
        {#if editing}
          <form
            class="flex items-center gap-1.5"
            onsubmit={(e) => {
              e.preventDefault();
              save();
            }}
          >
            <!-- svelte-ignore a11y_autofocus -->
            <input class="input h-7 flex-1 text-sm" bind:value={label} placeholder={live.host?.hostname} autofocus maxlength="64" onkeydown={(e) => e.key === 'Escape' && (editing = false)} />
            <button type="submit" class="rounded-md bg-accent p-1 text-accent-fg disabled:opacity-50" disabled={saving} aria-label="Save"><Check size={14} strokeWidth={3} /></button>
          </form>
        {:else}
          <button class="flex items-center gap-1.5 text-left text-sm text-fg hover:text-accent" onclick={edit}>
            <span class="truncate">{hostName()}</span>
            <span class="text-[11px] text-fg-faint">edit</span>
          </button>
        {/if}
      </div>
      {#each steps as s (s.title)}
        <a href={s.href} class="group flex min-h-[4.5rem] items-center gap-3 px-5 py-3 transition-colors hover:bg-raised/50">
          <span class="flex h-5 w-5 shrink-0 items-center justify-center rounded-full border {s.done ? 'border-ok bg-ok/15 text-ok' : 'border-line-strong text-fg-faint'}">
            {#if s.done}<Check size={11} strokeWidth={3} />{/if}
          </span>
          <span class="min-w-0 flex-1">
            <span class="block text-[11px] font-semibold tracking-wider text-fg-faint uppercase">{s.title}</span>
            <span class="block truncate text-sm {s.done ? 'text-fg' : 'text-fg-muted'}">{s.value}</span>
          </span>
          <span class="inline-flex items-center gap-1 text-xs text-fg-faint transition-colors group-hover:text-accent">{s.action}<ArrowRight size={12} /></span>
        </a>
      {/each}
    </div>
  </section>
{/if}
