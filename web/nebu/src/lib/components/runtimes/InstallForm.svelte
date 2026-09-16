<script lang="ts">
  import { untrack } from 'svelte';
  import { api, message } from '$lib/api';
  import { live, startedTask } from '$lib/state.svelte';
  import { ok } from '$lib/toast.svelte';
  import { tail } from '$lib/format';
  import { methodVerb, methodDoing } from '$lib/runtimes';
  import { InstallKind, type RuntimeStatus } from '$proto/runtime_pb';
  import { ConfigType } from '$proto/source_pb';
  import Segmented from '../ui/Segmented.svelte';
  import Button from '../ui/Button.svelte';
  import SettingsFields from './SettingsFields.svelte';

  // One install of a runtime by the method chosen and the settings it takes: an adopt records the binary
  // at once, the other methods start a task the page follows
  let { status }: { status: RuntimeStatus } = $props();

  const runtime = $derived(status.runtime);
  const options = $derived(status.installs);
  // The first method whose defaults run here, else the first
  let method = $state(untrack(() => (status.installs.find((o) => o.unmet.length === 0) ?? status.installs[0])?.method?.id ?? ''));
  let values = $state<Record<string, string>>({});
  let busy = $state(false);
  let error = $state('');

  const current = $derived(options.find((o) => o.method?.id === method) ?? options[0]);
  const kind = $derived(current?.method?.kind ?? InstallKind.UNSPECIFIED);
  const fields = $derived(current?.fields ?? []);
  const missing = $derived(fields.filter((f) => f.required && !f.default && !(values[f.name] ?? '').trim()));
  // What keeps the method from running is said only when no field can put it right: there are no
  // fields, or a required one has nothing to choose from and no path to pick
  const stuck = $derived(current?.unmet ?? []);
  const showStuck = $derived(stuck.length > 0 && (fields.length === 0 || fields.some((f) => f.required && !f.default && !f.choices.length && f.type !== ConfigType.PATH)));

  // Switching methods starts the settings over, and any edit clears the last refusal
  $effect(() => {
    void method;
    untrack(() => (values = {}));
  });
  $effect(() => {
    void values;
    untrack(() => (error = ''));
  });

  async function submit() {
    if (!runtime || !current?.method || busy) return;
    busy = true;
    error = '';
    const settings: Record<string, string> = {};
    for (const [k, v] of Object.entries(values)) if (v.trim()) settings[k] = v.trim();
    try {
      if (kind === InstallKind.ADOPTED) {
        const pathField = fields.find((f) => f.type === ConfigType.PATH);
        const r = await api.runtimes.adoptInstall({ runtimeId: runtime.id, path: pathField ? (settings[pathField.name] ?? '') : '' });
        if (r.install) {
          live.installs.set(r.install.id, r.install);
          ok(`Adopted ${r.install.version || tail(r.install.path)}`, r.install.path);
        }
      } else {
        const r = await api.runtimes.install({ runtimeId: runtime.id, method: current.method.id, settings });
        startedTask(`${methodDoing(kind)} ${runtime.name}`, `Installed ${runtime.name}`, undefined, r.task);
      }
      values = {};
    } catch (err) {
      error = message(err);
    } finally {
      busy = false;
    }
  }
</script>

<form
  class="flex max-w-lg flex-col gap-5"
  onsubmit={(e) => {
    e.preventDefault();
    if (!missing.length) submit();
  }}
>
  {#if options.length > 1}
    <Segmented class="self-start" tabs={options.map((o) => ({ id: o.method?.id ?? '', label: methodVerb(o.method?.kind ?? InstallKind.UNSPECIFIED) }))} bind:value={method} />
  {/if}
  {#if fields.length}
    <SettingsFields {fields} bind:values idPrefix="install-{method}" />
  {/if}
  {#if showStuck}
    <p class="text-sm text-fg-muted">{stuck.join('. ')}</p>
  {/if}
  {#if error}<div class="note note-bad">{error}</div>{/if}
  <div>
    <Button type="submit" variant="primary" loading={busy} disabled={missing.length > 0 || !current?.method}>{methodVerb(kind)}</Button>
  </div>
</form>
