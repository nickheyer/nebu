<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';

	let {
		dirty,
		saving = false,
		message,
		error,
		label = 'Save',
		disabled = false,
		onsave,
		ondiscard
	}: { dirty: boolean; saving?: boolean; message?: string; error?: string; label?: string; disabled?: boolean; onsave: () => unknown; ondiscard: () => void } = $props();
</script>

{#if dirty}
	<div class="savebar">
		<span class="msg" class:error-text={error !== undefined}>{error ?? message ?? 'Unsaved changes'}</span>
		<span class="fill"></span>
		<NeoButton text onclick={ondiscard} disabled={saving}>Discard</NeoButton>
		<NeoButton class="primary" elevation={2} loading={saving} {disabled} onclick={() => onsave()}>{label}</NeoButton>
	</div>
{/if}

<style>
	.savebar {
		position: sticky;
		bottom: 0;
		z-index: 2;
		display: flex;
		gap: 12px;
		align-items: center;
		margin-top: 8px;
		padding: 12px 16px;
		background: var(--neo-background-color);
		border-top: 1px solid var(--nebu-border);
	}
	.msg {
		font-size: 13px;
		color: var(--nebu-text-muted);
	}
</style>
