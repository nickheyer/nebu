<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoMenu, type NeoMenuItem } from '#lib/neo/floating/menu/index.js';
	import { NeoPopConfirm } from '#lib/neo/floating/tooltips/index.js';
	import DotsThreeIcon from 'phosphor-svelte/lib/DotsThreeIcon';

	export type MenuItem = {
		label: string;
		onclick?: () => unknown;
		href?: string;
		disabled?: boolean;
		danger?: boolean;
		// A question the item asks before it runs, on a confirm anchored to the menu button
		confirm?: string;
		// Fields the confirm shows under its question, such as a switch the action reads
		confirmBody?: Snippet;
		divider?: boolean;
		description?: string;
	};

	let { items, label = 'More' }: { items: MenuItem[]; label?: string } = $props();

	let trigger = $state<HTMLElement>();
	let pending = $state<MenuItem | undefined>(undefined);
	let confirmOpen = $state(false);

	const menu = $derived(
		items.map(
			(it, i): NeoMenuItem => ({
				value: i,
				label: it.label,
				description: it.description,
				disabled: it.disabled,
				color: it.danger ? 'error' : undefined,
				divider: it.divider,
				href: it.href
			})
		)
	);

	function onSelect(item: NeoMenuItem) {
		const it = items[item.value as number];
		if (!it || it.href) return;
		if (it.confirm) {
			pending = it;
			confirmOpen = true;
			return;
		}
		void it.onclick?.();
	}

	async function confirmed() {
		const it = pending;
		pending = undefined;
		await it?.onclick?.();
	}
</script>

<span class="menu" bind:this={trigger}>
	<NeoMenu items={menu} {onSelect} placement="bottom-end">
		<NeoButton text aria-label={label} title={label}>
			{#snippet icon()}<DotsThreeIcon size={20} weight="bold" />{/snippet}
		</NeoButton>
	</NeoMenu>
</span>
{#if pending}
	<NeoPopConfirm bind:open={confirmOpen} target={trigger} elevation={2} tooltipProps={{ placement: 'bottom-end' }} onConfirm={confirmed} onCancel={() => (pending = undefined)} confirmProps={{ label: pending.label, color: pending.danger ? 'error' : 'success' }}>
		{#snippet tooltip()}
			<span class="question">{pending?.confirm}</span>
			{#if pending?.confirmBody}<div class="body">{@render pending.confirmBody()}</div>{/if}
		{/snippet}
	</NeoPopConfirm>
{/if}

<style>
	.menu {
		display: inline-flex;
	}
	.question {
		display: block;
		max-width: 32ch;
		padding: 4px 4px 0;
		font-size: 13px;
	}
	.body {
		padding: 8px 4px 4px;
	}
</style>
