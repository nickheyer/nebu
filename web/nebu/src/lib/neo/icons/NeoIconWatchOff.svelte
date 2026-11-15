<script lang="ts">
  import type { NeoIconWatchProps } from '#lib/neo/icons/neo-icon.model.js';

  import { closed, gaze, open, radius } from '#lib/neo/icons/neo-icon-watch.utils.js';

  const { size = '1em', scale = 1, stroke = 1.5, enter = true, ...rest }: NeoIconWatchProps = $props();

  const mask = $props.id();
</script>

<svg
  xmlns="http://www.w3.org/2000/svg"
  width={size}
  height={size}
  viewBox="0 0 24 24"
  style:scale
  {...rest}
>
  <mask id={mask}>
    <g fill="none" stroke="#fff" stroke-linecap="round" stroke-linejoin="round" stroke-width={stroke}>
      <g transform="translate(12 12)">
        <g transform={enter ? 'scale(0)' : undefined}>
          {#if enter}
            <animateTransform attributeName="transform" type="scale" dur="6s" keyTimes="0;0.03;0.97;1" repeatCount="indefinite" values="0;1;1;0" />
          {/if}
          <circle {@attach gaze(stroke)} r={radius} fill="#fff" stroke="none" />
        </g>
      </g>
      <path d={enter ? closed : open}>
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
      <path stroke="#000" stroke-dasharray="28" stroke-dashoffset={enter ? 28 : 0} d="M0 11h24" transform="rotate(45 12 12)">
        {#if enter}
          <animate fill="freeze" attributeName="stroke-dashoffset" begin="0.5s" dur="0.4s" values="28;0" />
        {/if}
      </path>
      <path stroke-dasharray="28" stroke-dashoffset={enter ? 28 : 0} d="M-1 13h24" transform="rotate(45 12 12)">
        {#if enter}
          <animate attributeName="d" dur="6s" repeatCount="indefinite" values="M-1 13h24;M1 13h24;M-1 13h24" />
          <animate fill="freeze" attributeName="stroke-dashoffset" begin="0.5s" dur="0.4s" values="28;0" />
        {/if}
      </path>
    </g>
  </mask>
  <rect width="24" height="24" fill="currentColor" mask="url(#{mask})" />
</svg>
