<script lang="ts">
  let { size = 24, class: cls = '' }: { size?: number; class?: string } = $props();
</script>

<svg width={size} height={size} viewBox="0 0 32 32" class={cls} aria-hidden="true">
  <path d="M4 4H10V8H8V24H10V28H4Z" fill="currentColor" />
  <path d="M28 4H22V8H24V24H22V28H28Z" fill="currentColor" />
  <path d="M9.5 4H14.7L22.5 23.2V28H17.3L9.5 8.8Z" class="text-accent" fill="currentColor" />
</svg>
