<script lang="ts">
	import Tip from '#lib/ui/Tip.svelte';
	import CheckIcon from 'phosphor-svelte/lib/CheckIcon';
	import HourglassIcon from 'phosphor-svelte/lib/HourglassIcon';
	import ProhibitIcon from 'phosphor-svelte/lib/ProhibitIcon';
	import XIcon from 'phosphor-svelte/lib/XIcon';

	export type MarkState = 'ok' | 'failed' | 'skipped' | 'active';

	// A result mark, check for ok, cross for failed, prohibit for skipped, hourglass while active
	let { state, label, tip }: { state: MarkState; label: string; tip?: string } = $props();
</script>

<Tip text={tip} tag="span">
	<span class="mark {state}" role="img" aria-label={label}>
		{#if state === 'ok'}<CheckIcon size={20} />{:else if state === 'failed'}<XIcon size={20} />{:else if state === 'active'}<HourglassIcon size={20} />{:else}<ProhibitIcon size={20} />{/if}
	</span>
</Tip>

<style>
	.mark {
		display: inline-flex;
		flex: 0 0 auto;
	}
	.ok {
		color: var(--neo-color-success);
	}
	.failed {
		color: var(--neo-color-error);
	}
	.active {
		color: var(--neo-color-warning);
	}
	.skipped {
		color: var(--nebu-text-muted);
	}
</style>
