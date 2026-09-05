<script lang="ts">
  let { rows = 3, class: cls = '' }: { rows?: number; class?: string } = $props();
</script>

<div class="flex flex-col gap-3 {cls}" aria-busy="true">
  {#each Array(rows) as _, i (i)}
    <div class="skeleton h-3.5" style="width: {88 - (i % 3) * 18}%"></div>
  {/each}
</div>
