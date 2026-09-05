<script lang="ts">
  // The mark: the Nebuchadnezzar in side elevation, bow left, cockpit lit and hover pads glowing in the accent
  let { size = 24, class: cls = '' }: { size?: number; class?: string } = $props();
</script>

<svg width={size} height={size} viewBox="0 0 32 32" class={cls} aria-hidden="true">
  <path d="M2 15L5 11.5H7L8 7.5C8.2 6.9 8.8 6.5 9.5 6.5H14.5C15.5 6.5 16 7.4 16.5 8.5L17.5 10H28C30 10 31 11.5 31 13.5V15.5C31 17 30 18 28.5 18H6C4 18 2 16.5 2 15Z" fill="currentColor" />
  <g class="text-accent" fill="currentColor">
    <rect x="9" y="8.6" width="5.6" height="1.6" rx="0.8" />
    <rect x="5.5" y="19.2" width="5.5" height="2.4" rx="1.2" />
    <rect x="12.9" y="19.2" width="5.5" height="2.4" rx="1.2" />
    <rect x="20.3" y="19.2" width="5.5" height="2.4" rx="1.2" />
    <rect x="3.5" y="24.2" width="24.5" height="1.4" rx="0.7" opacity="0.3" />
  </g>
</svg>
