<script lang="ts">
  // The mark: a hovercraft hull over its lift pads, the pads in the accent
  let { size = 22, class: cls = '' }: { size?: number; class?: string } = $props();
</script>

<svg width={size} height={size} viewBox="0 0 32 32" class={cls} aria-hidden="true">
  <path d="M5 13.5H16.5C21 13.5 25 15.5 27 19.5V20.5A2 2 0 0 1 25 22.5H7.5A2.5 2.5 0 0 1 5 20Z" fill="currentColor" />
  <path d="M8 9.5H13.5A2 2 0 0 1 15.5 11.5V13.5H8Z" fill="currentColor" />
  <g class="text-accent" fill="currentColor">
    <rect x="7" y="24.5" width="7.5" height="2.6" rx="1.3" />
    <rect x="17.5" y="24.5" width="7.5" height="2.6" rx="1.3" />
    <rect x="5" y="28.2" width="22" height="1.6" rx="0.8" opacity="0.3" />
  </g>
</svg>
