<script lang="ts">
  import { inMotion, stateLabel, tone as toneOf, type Tone } from '$lib/format';

  // A colored dot and a word, the one way state is shown, from a generated enum or given outright
  let {
    values,
    value,
    tone,
    label,
    pulse,
    class: cls = ''
  }: { values?: Record<number, string>; value?: number; tone?: Tone; label?: string; pulse?: boolean; class?: string } = $props();

  const text = $derived(label ?? (values ? stateLabel(values, value) : ''));
  const t = $derived<Tone>(tone ?? (values ? toneOf(text) : 'neutral'));
  const moving = $derived(pulse ?? (values ? inMotion(values, value) : false));
  const dots: Record<Tone, string> = { ok: 'text-ok', warn: 'text-warn', bad: 'text-bad', info: 'text-info', accent: 'text-accent', neutral: 'text-fg-faint' };
  const words: Record<Tone, string> = { ok: 'text-fg', warn: 'text-fg', bad: 'text-bad', info: 'text-fg', accent: 'text-fg', neutral: 'text-fg-muted' };
</script>

<span class="inline-flex items-center gap-1.5 whitespace-nowrap {cls}">
  <span class="dot {dots[t]} {moving ? 'pulse' : ''}"></span>
  <span class="text-sm {words[t]}">{text}</span>
</span>
