<script lang="ts">
	// How many of something are set, a number in a filled dot
	let {
		n,
		// Follows words on its line, a space off them
		after = false
	}: { n: number; after?: boolean } = $props();
</script>

<span class="count" class:after>{n}</span>

<style>
	/* The line is the height of its digits alone. One as tall as the text around it outgrows the dot and drops the number below centre. */
	.count {
		display: inline-flex;
		flex: 0 0 auto;
		align-items: center;
		justify-content: center;
		min-width: 18px;
		height: 18px;
		padding: 0 5px;
		font-size: 11px;
		font-variant-numeric: lining-nums proportional-nums;
		line-height: 1;
		color: var(--neo-background-color);
		background: var(--neo-color-primary);
		border-radius: 9px;
	}
	.after {
		margin-left: 6px;
	}
</style>
