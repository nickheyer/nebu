<script lang="ts">
  import { ArrowDownToLine, Heart, Lock, EyeOff, Database, Cpu, HardDrive } from '@lucide/svelte';
  import type { SearchHit, SourceCapabilities } from '$proto/source_pb';
  import type { RuntimeStatus } from '$proto/runtime_pb';
  import { live, clock } from '$lib/state.svelte';
  import { ago, bytes, count, params as fmtParams } from '$lib/format';
  import { displayTags, facetValueLabel, formatBlurb, hitSize, runtimesFor } from '$lib/catalog';

  let {
    hit,
    caps,
    runtimes = [],
    selected = false,
    compact = false,
    onOpen
  }: { hit: SearchHit; caps?: SourceCapabilities; runtimes?: RuntimeStatus[]; selected?: boolean; compact?: boolean; onOpen: (hit: SearchHit) => void } = $props();

  const stored = $derived([...live.models.values()].some((m) => m.sourceId === hit.sourceId && m.repo === hit.repo));
  const size = $derived(hitSize(hit));
  const sizes = $derived([...new Set((hit.extra['sizes'] ?? '').split(',').filter(Boolean))]);
  const tags = $derived(displayTags(hit, compact ? 2 : 4));
  const title = $derived(hit.name && hit.name !== hit.repo ? hit.name : hit.repo);
  const runners = $derived(runtimesFor(hit.formats, runtimes));
  const updated = $derived(hit.updatedAt ? ago(hit.updatedAt, clock.now) : '');

  const chipTones = {
    accent: 'bg-accent/12 text-accent',
    info: 'border border-info/30 bg-info/10 text-info',
    mono: 'border border-line bg-sunken font-mono text-fg-muted',
    plain: 'bg-raised text-fg-muted',
    faint: 'bg-raised text-fg-faint'
  };
</script>

{#snippet chip(text: string, tone: keyof typeof chipTones, title: string)}
  <span class="inline-flex max-w-full items-center gap-1 rounded-md px-1.5 py-0.5 text-[10.5px] font-medium {chipTones[tone]}" {title}>{text}</span>
{/snippet}

{#snippet flags()}
  {#if stored}
    <span class="inline-flex shrink-0 items-center gap-1 rounded-full border border-ok/25 bg-ok/12 px-1.5 text-[10.5px] font-medium text-ok" title="A weight group from this repository is already in the store"><Database size={10} /> in store</span>
  {/if}
  {#if hit.gated}
    <span class="inline-flex shrink-0 items-center gap-1 rounded-full border border-warn/25 bg-warn/12 px-1.5 text-[10.5px] font-medium text-warn" title="Gated: accept the license on the source's site and give the daemon a token before pulling"><Lock size={10} /> gated</span>
  {/if}
  {#if hit.private}
    <span class="inline-flex shrink-0 items-center gap-1 rounded-full border border-line bg-raised px-1.5 text-[10.5px] font-medium text-fg-muted" title="Private: only visible with a token that has access"><EyeOff size={10} /> private</span>
  {/if}
  {#if hit.extra['nsfw']}
    <span class="inline-flex shrink-0 items-center rounded-full border border-bad/25 bg-bad/10 px-1.5 text-[10.5px] font-medium text-bad" title="Marked adult content by the source">NSFW</span>
  {/if}
{/snippet}

{#snippet chips()}
  {#if hit.task}{@render chip(facetValueLabel(caps, 'task', hit.task), 'accent', 'What the model is for')}{/if}
  {#each runners as r (r.id)}
    <span class="inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-[10.5px] font-medium {r.compatible ? 'border-info/30 bg-info/10 text-info' : 'border-line bg-sunken text-fg-faint'}" title={r.compatible ? `${r.name} can serve this format on this host` : `${r.name} serves this format but is not compatible with this host`}>
      <Cpu size={10} /> runs on {r.name}
    </span>
  {/each}
  {#each hit.formats as f (f)}{@render chip(f, 'mono', formatBlurb[f] ?? `${f} weight files`)}{/each}
  {#if size.kind === 'params'}{@render chip(`${fmtParams(size.value)} params`, 'plain', `${fmtParams(size.value)} parameters. More parameters usually means smarter answers and more memory needed`)}{/if}
  {#each sizes as s (s)}{@render chip(s, 'info', 'A size this model is published in, in parameters')}{/each}
  {#if hit.extra['quant']}{@render chip(hit.extra['quant'], 'mono', 'Quantization, how compactly the weights are stored')}{/if}
  {#if hit.extra['base_model']}{@render chip(hit.extra['base_model'], 'plain', 'Base model this was made for')}{/if}
  {#if hit.extra['architecture'] && !compact}{@render chip(hit.extra['architecture'], 'faint', 'Model architecture')}{/if}
  {#each tags as t (t)}{@render chip(t, 'faint', 'Tag from the source')}{/each}
{/snippet}

{#snippet stats()}
  {#if hit.downloads > 0n}<span class="inline-flex items-center gap-1" title="Downloads counted by the source"><ArrowDownToLine size={11} />{count(hit.downloads)} downloads</span>{/if}
  {#if hit.likes > 0n}<span class="inline-flex items-center gap-1" title="Likes on the source"><Heart size={11} />{count(hit.likes)} likes</span>{/if}
  {#if size.kind === 'bytes'}<span class="inline-flex items-center gap-1" title="Size of the default weights"><HardDrive size={11} />{bytes(size.value, 1)}</span>{/if}
  {#if hit.license}<span class="truncate" title="License">{hit.license} license</span>{/if}
{/snippet}

{#if compact}
  <button
    class="group flex w-full items-center gap-3 rounded-lg border px-3 py-2 text-left transition-colors {selected ? 'border-accent/60 bg-accent/5' : 'border-line bg-surface hover:border-line-strong hover:bg-raised/40'}"
    onclick={() => onOpen(hit)}
  >
    <div class="w-64 min-w-0 shrink-0">
      <div class="flex items-center gap-1.5">
        <span class="truncate text-sm font-semibold text-fg" title={hit.repo}>{title}</span>
        {@render flags()}
      </div>
      <div class="truncate text-xs text-fg-faint">{hit.author}{#if hit.name && hit.name !== hit.repo}<span class="font-mono"> · {hit.repo}</span>{/if}</div>
    </div>
    <div class="flex min-w-0 flex-1 flex-wrap items-center gap-1.5">{@render chips()}</div>
    <div class="hidden shrink-0 items-center gap-3 text-[11.5px] text-fg-faint tabular-nums lg:flex">
      {@render stats()}
      {#if updated}<span title="Last updated on the source">{updated}</span>{/if}
    </div>
  </button>
{:else}
  <button
    class="group flex w-full flex-col gap-2.5 rounded-xl border p-4 text-left transition-colors {selected ? 'border-accent/60 bg-accent/5' : 'border-line bg-surface hover:border-line-strong hover:bg-raised/40'}"
    onclick={() => onOpen(hit)}
  >
    <div class="flex w-full items-start gap-2">
      <div class="min-w-0 flex-1">
        <div class="truncate text-sm font-semibold text-fg" title={hit.repo}>{title}</div>
        <div class="truncate text-xs text-fg-faint">
          {#if hit.author}by {hit.author}{/if}
          {#if hit.name && hit.name !== hit.repo}<span class="font-mono"> · {hit.repo}</span>{/if}
        </div>
      </div>
      <div class="flex shrink-0 flex-wrap justify-end gap-1">{@render flags()}</div>
    </div>

    {#if hit.description}
      <p class="line-clamp-2 text-xs leading-5 text-fg-muted">{hit.description}</p>
    {/if}

    <div class="flex flex-wrap items-center gap-1.5">{@render chips()}</div>

    <div class="mt-auto flex w-full flex-wrap items-center gap-x-3 gap-y-1 text-[11.5px] text-fg-faint tabular-nums">
      {@render stats()}
      {#if updated}<span class="ml-auto" title="Last updated on the source">updated {updated}</span>{/if}
    </div>
  </button>
{/if}
