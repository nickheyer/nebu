<script lang="ts">
  import type { NeoIconWatchProps } from '#lib/neo/icons/neo-icon.model.js';

  import { closed, gaze, open, radius } from '#lib/neo/icons/neo-icon-watch.utils.js';

  const { size = '1em', scale = 1, stroke = 1.5, enter = true, ...rest }: NeoIconWatchProps = $props();
</script>

<svg
  xmlns="http://www.w3.org/2000/svg"
  width={size}
  height={size}
  viewBox="0 0 24 24"
  style:scale
  {...rest}
>
  <g transform="translate(12 12)">
    <g transform={enter ? 'scale(0)' : undefined}>
      {#if enter}
        <animateTransform attributeName="transform" type="scale" dur="6s" keyTimes="0;0.03;0.97;1" repeatCount="indefinite" values="0;1;1;0" />
      {/if}
      <circle {@attach gaze(stroke)} r={radius} fill="currentColor" />
    </g>
  </g>
  <path
    fill="none"
    stroke="currentColor"
    stroke-linecap="round"
    stroke-linejoin="round"
    stroke-width={stroke}
    d={enter ? closed : open}
  >
    {#if enter}
      <animate
        attributeName="d"
        dur="6s"
        keyTimes="0;0.03;0.97;1"
        repeatCount="indefinite"
        values="{closed};{open};{open};{closed}"
      />
    {/if}
  </path>
</svg>
