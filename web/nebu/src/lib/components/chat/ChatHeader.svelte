<script lang="ts">
  import { RotateCcw, Trash2, PanelRightClose, PanelRightOpen, Maximize2, Minimize2, Ghost, MessageSquarePlus } from '@lucide/svelte';
  import { theater, toggleTheater } from '$lib/theater.svelte';
  import { instanceLive, runtimeName, clock } from '$lib/state.svelte';
  import { enumLabel, duration } from '$lib/format';
  import { RouteState, type Route } from '$proto/gateway_pb';
  import type { Instance } from '$proto/instance_pb';
  import type { Install } from '$proto/runtime_pb';
  import type { Slot } from '$proto/slot_pb';
  import Select, { type SelectItem } from '$lib/components/ui/Select.svelte';
  import Segmented, { type Segment } from '$lib/components/ui/Segmented.svelte';
  import State from '$lib/components/ui/State.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';

  let {
    model = $bindable(''),
    modelItems,
    chosen,
    media,
    canImage,
    canVideo,
    mode = $bindable('image'),
    modeItems,
    instance,
    install,
    slot,
    vision,
    inspector = $bindable(true),
    hasTurns,
    busy,
    saved,
    ghost,
    onregenerate,
    onclear,
    onghost,
    onnew
  }: {
    model?: string;
    modelItems: SelectItem[];
    chosen: Route | undefined;
    media: boolean;
    canImage: boolean;
    canVideo: boolean;
    mode?: 'image' | 'video';
    modeItems: Segment[];
    instance: Instance | undefined;
    install: Install | undefined;
    slot: Slot | undefined;
    // Whether the model takes images, undefined when unknown
    vision: boolean | undefined;
    inspector?: boolean;
    hasTurns: boolean;
    busy: boolean;
    saved: boolean;
    ghost: boolean;
    onregenerate: () => void;
    onclear: () => void;
    onghost: () => void;
    onnew: () => void;
  } = $props();
</script>

<header class="flex items-center gap-3 border-b border-line px-4 py-2.5">
  <Select size="sm" class="w-80 max-w-[40%] shrink-0" mono bind:value={model} label="Model" items={modelItems} />
  {#if chosen}
    <State values={RouteState} value={chosen.state} class="shrink-0" />
    {#if media && canImage && canVideo}
      <Segmented bind:value={mode} tabs={modeItems} class="shrink-0" />
    {/if}
    {#if instance}
      <span class="hidden min-w-0 flex-1 items-center gap-x-3 overflow-hidden text-xs whitespace-nowrap text-fg-muted md:flex">
        <span class="kv"><span>runtime</span><span>{runtimeName(instance.runtimeId)}{install?.version ? ` ${install.version}` : ''}</span></span>
        {#if instance.params.n_ctx}<span class="kv"><span>ctx</span><span>{instance.params.n_ctx}</span></span>{/if}
        {#if media}<span class="kv"><span>makes</span><span>{canImage && canVideo ? 'images, video' : canVideo ? 'video' : 'images'}</span></span>{:else if vision}<span class="kv" title="The model has a vision encoder"><span>sees</span><span>images</span></span>{/if}
        {#if slot}<a class="kv link" href="/slots/{slot.id}"><span>slot</span><span>{slot.name}</span></a>{:else}<a class="link" href="/instances/{instance.id}">Open instance</a>{/if}
        {#if instanceLive(instance)}<span class="kv"><span>up</span><span>{duration(instance.readyAt ?? instance.createdAt, undefined, clock.now)}</span></span>{/if}
      </span>
    {/if}
  {/if}
  <span class="ml-auto flex shrink-0 items-center gap-1">
    {#if theater.on}
      <!-- The page header is hidden on stage, so its actions live here instead. -->
      <IconButton size="sm" icon={Ghost} variant={ghost ? 'primary' : 'secondary'} aria-pressed={ghost} label={ghost ? 'Ghost mode on. Turn it off to save chats again.' : 'Ghost mode: a chat that is never saved'} onclick={onghost} />
      <IconButton size="sm" icon={MessageSquarePlus} label="New chat" onclick={onnew} disabled={busy} />
      <span class="mx-1 h-4 w-px bg-line" aria-hidden="true"></span>
    {/if}
    <IconButton size="sm" icon={RotateCcw} label={media ? 'Make the last prompt again' : 'Regenerate the last answer'} onclick={onregenerate} disabled={busy || !hasTurns} />
    <IconButton size="sm" icon={Trash2} label={media ? 'Clear the generations shown' : saved ? 'Delete this chat' : 'Discard this chat'} onclick={onclear} disabled={!hasTurns || busy} />
    <IconButton size="sm" icon={inspector ? PanelRightClose : PanelRightOpen} label={inspector ? 'Hide the side panel' : 'Show the side panel'} onclick={() => (inspector = !inspector)} />
    <IconButton size="sm" icon={theater.on ? Minimize2 : Maximize2} aria-pressed={theater.on} label={theater.on ? 'Leave theater mode (Esc)' : 'Theater mode: hide everything but the chat'} onclick={toggleTheater} />
  </span>
</header>
