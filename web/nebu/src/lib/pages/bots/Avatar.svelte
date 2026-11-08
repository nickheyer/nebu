<script lang="ts">
	import RobotIcon from 'phosphor-svelte/lib/RobotIcon';

	// A bot or persona picture in a circle, a robot when there is none
	let { url, size }: { url: string | undefined; size: 20 | 24 | 40 } = $props();
</script>

{#if url}
	<img class="avatar" src={url} alt="" width={size} height={size} />
{:else}
	<span class="avatar blank" style:width="{size}px" style:height="{size}px" aria-hidden="true"><RobotIcon size={Math.round(size * 0.6)} /></span>
{/if}

<style>
	.avatar {
		display: inline-flex;
		flex: 0 0 auto;
		align-items: center;
		justify-content: center;
		border-radius: 50%;
		object-fit: cover;
	}
	.blank {
		color: var(--nebu-text-muted);
		background: var(--nebu-surface-sunken);
	}
</style>
