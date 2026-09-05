<script lang="ts">
  import { live, liveInstances, hostName, updateSettings } from '$lib/state.svelte';
  import { Check, X, ArrowRight } from '@lucide/svelte';

  // The three steps between a fresh host and a model answering, shown until they are done or dismissed
  let label = $state('');
  let editing = $state(false);
  let saving = $state(false);

  const installs = $derived(live.installs.size);
  const models = $derived(live.models.size);
  const running = $derived(liveInstances().length);
  const complete = $derived(installs > 0 && models > 0 && running > 0);
  const shown = $derived(live.ready && !!live.settings && !live.settings.setupDismissed && !complete);

  const steps = $derived([
    { title: 'Install a runtime', value: installs ? `${installs} installed` : 'None installed', done: installs > 0, href: '/runtimes', action: 'Runtimes' },
    { title: 'Pull a model', value: models ? `${models} in the library` : 'None in the library', done: models > 0, href: '/catalog', action: 'Discover' },
    { title: 'Run it in a slot', value: running ? `${running} running` : 'Nothing running', done: running > 0, href: models ? '/store' : '/catalog', action: models ? 'Library' : 'Discover' }
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
  <section class="card p-5" aria-label="Setup">
    <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
      <h2 class="text-base font-semibold text-fg">Set up {hostName() || 'this host'}</h2>
      {#if editing}
        <form
          class="flex items-center gap-2"
          onsubmit={(e) => {
            e.preventDefault();
            save();
          }}
        >
          <!-- svelte-ignore a11y_autofocus -->
          <input class="input h-8 w-56" bind:value={label} placeholder={live.host?.hostname} autofocus maxlength="64" aria-label="Host label" onkeydown={(e) => e.key === 'Escape' && (editing = false)} />
          <button type="submit" class="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-accent text-accent-fg disabled:opacity-50" disabled={saving} aria-label="Save"><Check size={15} strokeWidth={3} /></button>
        </form>
      {:else}
        <button type="button" class="text-sm text-accent hover:underline" onclick={edit}>Name this host</button>
      {/if}
      <button class="-mr-2 ml-auto rounded-lg p-1.5 text-fg-faint transition-colors hover:bg-raised hover:text-fg" aria-label="Dismiss setup" title="Dismiss" onclick={dismiss}><X size={16} /></button>
    </div>
    <ol class="mt-4 grid gap-3 sm:grid-cols-3">
      {#each steps as s, i (s.title)}
        <li>
          <a href={s.href} class="group flex h-full items-center gap-3 rounded-lg border border-line bg-bg/40 px-4 py-3 transition-colors hover:border-line-strong">
            <span class="flex h-7 w-7 shrink-0 items-center justify-center rounded-full border text-xs font-semibold {s.done ? 'border-ok bg-ok/15 text-ok' : 'border-line-strong text-fg-muted'}">
              {#if s.done}<Check size={14} strokeWidth={3} />{:else}{i + 1}{/if}
            </span>
            <span class="min-w-0 flex-1">
              <span class="block truncate text-sm font-medium {s.done ? 'text-fg-muted line-through' : 'text-fg'}">{s.title}</span>
              <span class="block truncate text-xs text-fg-faint">{s.value}</span>
            </span>
            {#if !s.done}<span class="inline-flex items-center gap-1 text-xs text-fg-faint transition-colors group-hover:text-accent">{s.action}<ArrowRight size={12} /></span>{/if}
          </a>
        </li>
      {/each}
    </ol>
  </section>
{/if}
