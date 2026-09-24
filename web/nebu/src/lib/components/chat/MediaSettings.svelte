<script lang="ts">
  import { Dices } from '@lucide/svelte';
  import { figure, modeKey, namedChoice, type Capabilities } from '$lib/generate';
  import Field from '$lib/components/ui/Field.svelte';
  import Select from '$lib/components/ui/Select.svelte';
  import NumberInput from '$lib/components/ui/NumberInput.svelte';
  import RangeInput from '$lib/components/ui/RangeInput.svelte';
  import TextInput from '$lib/components/ui/TextInput.svelte';
  import TextArea from '$lib/components/ui/TextArea.svelte';
  import IconButton from '$lib/components/ui/IconButton.svelte';
  import Disclosure from '$lib/components/ui/Disclosure.svelte';
  import SwitchRow from '$lib/components/ui/SwitchRow.svelte';

  // Empty fields use model defaults and are omitted from the request.
  export interface MediaForm {
    negative: string;
    width: string;
    height: string;
    steps: string;
    cfg: string;
    seed: string;
    sampler: string;
    scheduler: string;
    n: string;
    frames: string;
    fps: string;
    strength: string;
    guidance: string;
    flowShift: string;
    clipSkip: string;
    vaeTiling: boolean;
    temporalTiling: boolean;
    highSteps: string;
    highCfg: string;
    format: string;
    loras: string;
  }

  let {
    form = $bindable(),
    mode,
    caps,
    hasInit
  }: { form: MediaForm; mode: 'image' | 'video'; caps: Capabilities | null; hasInit: boolean } = $props();

  const imageSizes = ['512x512', '768x768', '1024x1024', '1024x768', '768x1024', '1152x896', '896x1152', '1344x768', '768x1344'];
  const videoSizes = ['832x480', '480x832', '640x640', '1024x576', '576x1024', '1280x720', '720x1280'];
  const samplers = ['euler', 'euler_a', 'heun', 'dpm2', 'dpm++2s_a', 'dpm++2m', 'dpm++2mv2', 'ipndm', 'ipndm_v', 'lcm', 'ddim_trailing', 'tcd', 'res_multistep', 'res_2s', 'er_sde', 'euler_cfg_pp', 'euler_a_cfg_pp', 'euler_ge', 'dpm++2m_sde', 'dpm++2m_sde_bt', 'lms'];
  const schedulers = ['discrete', 'karras', 'exponential', 'ays', 'gits', 'sgm_uniform', 'simple', 'smoothstep', 'kl_optimal', 'lcm', 'bong_tangent', 'ltx2', 'logit_normal', 'flux2', 'flux', 'beta'];

  const d = $derived(caps?.defaults_by_mode?.[modeKey(mode)]);
  const sp = $derived(d?.sample_params);
  const hn = $derived(d?.high_noise_sample_params);
  const sizes = $derived(mode === 'video' ? videoSizes : imageSizes);
  const size = $derived(`${form.width}x${form.height}`);
  const sizeItems = $derived([...(sizes.includes(size) ? [] : [{ value: size, label: size }]), ...sizes.map((s) => ({ value: s, label: s }))]);
  // Show the default choice only when the server names one.
  const ownSampler = $derived(namedChoice(sp?.sample_method));
  const ownScheduler = $derived(namedChoice(sp?.scheduler));
  const samplerItems = $derived([...(ownSampler ? [{ value: '', label: ownSampler }] : []), ...(caps?.samplers?.length ? caps.samplers : samplers).map((s) => ({ value: s, label: s }))]);
  const schedulerItems = $derived([...(ownScheduler ? [{ value: '', label: ownScheduler }] : []), ...(caps?.schedulers?.length ? caps.schedulers : schedulers).map((s) => ({ value: s, label: s }))]);
  const formats = $derived(caps?.output_formats_by_mode?.[modeKey(mode)] ?? (mode === 'video' ? ['webm', 'webp', 'avi'] : ['png', 'jpeg', 'webp']));
  const ownFormat = $derived(d?.output_format ?? '');
  const formatItems = $derived([...(ownFormat ? [{ value: '', label: ownFormat }] : []), ...formats.filter((f) => f !== ownFormat).map((f) => ({ value: f, label: f }))]);
  // Hide flow shift when the model samples without it.
  const shifts = $derived(!!sp && !!sp.flow_shift);
  // Negative CLIP skip selects the model's default layer.
  const clipSkip = $derived(d && d.clip_skip >= 0 ? String(d.clip_skip) : '');
  const highSteps = $derived(hn && hn.sample_steps > 0 ? String(hn.sample_steps) : '');
  const loraNames = $derived((caps?.loras ?? []).map((l) => l.name));

  function newSeed() {
    form.seed = String(Math.floor(Math.random() * 2147483647));
  }
</script>

<div class="flex flex-col gap-5">
  <Field label="Negative prompt" for="gen-negative">
    <TextArea id="gen-negative" height="h-16" bind:value={form.negative} empty={d?.negative_prompt ?? ''} />
  </Field>

  <Field label="Size" for="gen-size">
    <Select
      id="gen-size"
      mono
      bind:value={
        () => size,
        (v) => {
          const [w, h] = v.split('x');
          form.width = w;
          form.height = h;
        }
      }
      items={sizeItems}
    />
  </Field>
  <div class="grid grid-cols-2 gap-3">
    <Field label="Width" for="gen-width"><NumberInput id="gen-width" integer min={caps?.limits?.min_width || 64} max={caps?.limits?.max_width || 4096} step={16} unit="px" bind:value={form.width} empty={figure(d?.width)} /></Field>
    <Field label="Height" for="gen-height"><NumberInput id="gen-height" integer min={caps?.limits?.min_height || 64} max={caps?.limits?.max_height || 4096} step={16} unit="px" bind:value={form.height} empty={figure(d?.height)} /></Field>
  </div>
  {#if mode === 'video'}
    <div class="grid grid-cols-2 gap-3">
      <Field label="Frames" for="gen-frames" description="Frame count must be 4n+1"><NumberInput id="gen-frames" integer min={1} max={401} step={4} bind:value={form.frames} empty={figure(d?.video_frames)} /></Field>
      <Field label="Frame rate" for="gen-fps"><NumberInput id="gen-fps" integer min={1} max={60} step={1} unit="fps" bind:value={form.fps} empty={figure(d?.fps)} /></Field>
    </div>
  {:else}
    <Field label="Images" for="gen-n"><NumberInput id="gen-n" integer min={1} max={caps?.limits?.max_batch_count || 8} step={1} bind:value={form.n} empty={figure(d?.batch_count)} /></Field>
  {/if}
  {#if hasInit && mode === 'image'}
    <Field label="Strength" for="gen-strength" description="Change from the input image. 1 ignores it.">
      <RangeInput id="gen-strength" min={0} max={1} step={0.05} bind:value={form.strength} empty={figure(d?.strength)} />
    </Field>
  {/if}
  <Field label="Steps" for="gen-steps"><RangeInput id="gen-steps" min={1} max={100} step={1} integer empty={figure(sp?.sample_steps)} bind:value={form.steps} /></Field>
  <Field label="Guidance scale" for="gen-cfg"><RangeInput id="gen-cfg" min={0} max={20} step={0.5} empty={figure(sp?.guidance.txt_cfg)} bind:value={form.cfg} /></Field>
  <Field label="Seed" for="gen-seed" description="Random for each request when empty">
    <div class="flex items-center gap-1.5">
      <TextInput id="gen-seed" class="flex-1" mono bind:value={form.seed} />
      <IconButton icon={Dices} label="Draw a seed" onclick={newSeed} />
    </div>
  </Field>
  <div class="grid grid-cols-2 gap-3">
    <Field label="Sampler" for="gen-sampler"><Select id="gen-sampler" mono bind:value={form.sampler} items={samplerItems} /></Field>
    <Field label="Scheduler" for="gen-scheduler"><Select id="gen-scheduler" mono bind:value={form.scheduler} items={schedulerItems} /></Field>
  </div>
  <Disclosure label="More">
    <div class="flex flex-col gap-3 pt-1">
      <div class="grid grid-cols-2 gap-3">
        <Field label="Distilled guidance" for="gen-guidance"><NumberInput id="gen-guidance" min={0} max={30} step={0.5} empty={figure(sp?.guidance.distilled_guidance)} bind:value={form.guidance} /></Field>
        {#if shifts}
          <Field label="Flow shift" for="gen-flow"><NumberInput id="gen-flow" min={0} max={20} step={0.05} empty={figure(sp?.flow_shift)} bind:value={form.flowShift} /></Field>
        {/if}
        <Field label="CLIP skip" for="gen-clip"><NumberInput id="gen-clip" integer min={-1} max={12} step={1} empty={clipSkip} bind:value={form.clipSkip} /></Field>
        <Field label="Output format" for="gen-format"><Select id="gen-format" mono bind:value={form.format} items={formatItems} /></Field>
      </div>
      {#if mode === 'video'}
        <div class="grid grid-cols-2 gap-3">
          <Field label="High noise steps" for="gen-hsteps" description="For a Wan 2.2 A14B pair"><NumberInput id="gen-hsteps" integer min={1} max={100} step={1} empty={highSteps} bind:value={form.highSteps} /></Field>
          <Field label="High noise guidance" for="gen-hcfg"><NumberInput id="gen-hcfg" min={0} max={20} step={0.5} empty={figure(hn?.guidance.txt_cfg)} bind:value={form.highCfg} /></Field>
        </div>
      {/if}
      <Field label="LoRAs" for="gen-loras" description={loraNames.length ? `Available: ${loraNames.join(', ')}. Use name:weight or high:name:weight for the high noise stage.` : "Use name:weight from the runtime's LoRA directory. Prefix with high: for the high noise stage."}>
        <TextInput id="gen-loras" mono bind:value={form.loras} empty={loraNames.length ? `${loraNames[0]}:0.8` : 'name:0.8, high:other:1'} />
      </Field>
      <SwitchRow bind:checked={form.vaeTiling} label="VAE tiling" />
      {#if mode === 'video'}
        <SwitchRow bind:checked={form.temporalTiling} label="Temporal tiling" />
      {/if}
    </div>
  </Disclosure>
</div>
