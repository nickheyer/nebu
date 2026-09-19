<script lang="ts">
  let { text, title, mono = true, class: cls = '' }: { text: string; title?: string; mono?: boolean; class?: string } = $props();
</script>

<span class="inline-flex h-5 shrink-0 items-center rounded-sm border border-line bg-raised/40 px-1.5 text-[11px] leading-none text-fg-muted {mono ? 'font-mono' : ''} {cls}" {title}>{text}</span>
