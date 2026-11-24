<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { NeoBadge } from '#lib/neo/badge/index.js';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoIconWatch } from '#lib/neo/icons/index.js';
	import { live } from '#lib/live/events.svelte.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import AccountMenu from '#lib/shell/AccountMenu.svelte';
	import Brand from '#lib/shell/Brand.svelte';
	import Command from '#lib/shell/Command.svelte';
	import { chrome, type HeaderTab } from '#lib/shell/chrome.svelte.js';
	import { morpheus } from '#lib/shell/morpheus.svelte.js';
	import { pageOf } from '#lib/shell/nav.js';
	import OriginLink from '#lib/ui/OriginLink.svelte';
	import CaretLeftIcon from 'phosphor-svelte/lib/CaretLeftIcon';
	import ListChecksIcon from 'phosphor-svelte/lib/ListChecksIcon';

	const head = $derived(chrome.header);
	const title = $derived(head?.title ?? pageOf(page.url.pathname)?.label ?? '');
	const tabs = $derived(head?.tabs ?? []);
	const active = $derived(live.activeTasks.length);
	const waiting = $derived(morpheus.waiting.length);

	let height = $state(0);
	$effect(() => {
		document.documentElement.style.setProperty('--nebu-top', `${height}px`);
	});

	// Which ends of the tab row hold tabs the window does not show
	let before = $state(false);
	let after = $state(false);

	// Follows the row's scroll for its faded edges and keeps the open tab in view
	function strip(node: HTMLElement) {
		const measure = () => {
			before = node.scrollLeft > 1;
			after = node.scrollLeft + node.clientWidth < node.scrollWidth - 1;
		};
		const ro = new ResizeObserver(measure);
		ro.observe(node);
		node.addEventListener('scroll', measure, { passive: true });
		$effect(() => {
			void head?.active;
			void tabs.length;
			node.querySelector<HTMLElement>('.tab.on')?.scrollIntoView({ block: 'nearest', inline: 'center' });
			measure();
		});
		return () => {
			ro.disconnect();
			node.removeEventListener('scroll', measure);
		};
	}

	function pick(t: HeaderTab) {
		if (t.id === head?.active) return;
		if (head?.ontab) head.ontab(t.id);
		else if (t.href) void goto(t.href);
	}
</script>

{#snippet tasks()}
	<NeoButton href="/tasks" text aria-label="Tasks">
		{#snippet icon()}<ListChecksIcon size={20} />{/snippet}
	</NeoButton>
{/snippet}

{#snippet eye()}
	<NeoButton text aria-label="Morpheus" title="Morpheus" aria-pressed={morpheus.open} color={morpheus.open ? 'primary' : undefined} onclick={() => (morpheus.open = !morpheus.open)}>
		{#snippet icon()}<NeoIconWatch size="20px" enter={false} />{/snippet}
	</NeoButton>
{/snippet}

<header class="top" class:mobile={viewport.mobile} bind:clientHeight={height}>
	<div class="frame">
		<div class="titlerow">
			{#if head?.back && viewport.mobile}
				<a class="back" href={head.back.href} aria-label="Back to {head.back.label}"><CaretLeftIcon size={20} /></a>
			{:else if head?.back}
				<a class="back" href={head.back.href}><CaretLeftIcon size={16} /><span>{head.back.label}</span></a>
				<span class="slash" aria-hidden="true">/</span>
			{:else if viewport.mobile}
				<Brand />
			{/if}
			{#key title}
				{#if head?.origin}
					<OriginLink href={head.origin.href} from={head.origin.label} external stacked={viewport.mobile}><h1 class="title">{title}</h1></OriginLink>
				{:else}
					<h1 class="title">{title}</h1>
				{/if}
			{/key}
			<span class="fill"></span>
			<Command />
			<div class="global">
				{#if session.reads}
					{#if active > 0}
						<NeoBadge value={String(active)} placement="top-right" size="small" color="primary">
							{@render tasks()}
						</NeoBadge>
					{:else}
						{@render tasks()}
					{/if}
					{#if morpheus.here && waiting > 0}
						<NeoBadge value={String(waiting)} placement="top-right" size="small" color="primary">
							{@render eye()}
						</NeoBadge>
					{:else if morpheus.here}
						{@render eye()}
					{/if}
				{/if}
				<AccountMenu />
			</div>
		</div>
		{#if tabs.length}
			<!-- Every section keeps its tabs on every width, a row too long for the window scrolling sideways with its cut edge faded -->
			<nav class="tabs" class:before class:after aria-label="Tabs" {@attach strip}>
				{#each tabs as t, i (t.id)}
					{@const on = t.id === head?.active}
					<a
						class="tab"
						class:on
						style:--i={i}
						href={t.href}
						aria-current={on ? 'page' : undefined}
						onclick={(e) => {
							if (head?.ontab || !t.href) {
								e.preventDefault();
								pick(t);
							}
						}}
					>
						{t.label}
					</a>
				{/each}
			</nav>
		{/if}
	</div>
</header>

<style>
	.top {
		position: sticky;
		top: 0;
		z-index: 5;
		background: var(--neo-background-color);
		border-bottom: 1px solid var(--nebu-border);
	}
	.frame {
		display: flex;
		flex-direction: column;
		max-width: var(--nebu-frame-max);
		margin: 0 auto;
		padding: 0 var(--nebu-gutter);
	}
	.titlerow {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		min-width: 0;
		height: 60px;
	}
	/* The title holds one line beside the page's controls, clipped where the bar runs out */
	.title {
		flex: 0 1 auto;
		min-width: 0;
		overflow: hidden;
		font-size: var(--nebu-text-title);
		font-weight: 500;
		white-space: nowrap;
		text-overflow: ellipsis;
		animation: nebu-reveal 240ms var(--nebu-settle) backwards;
	}
	.titlerow > :global(.brand) {
		flex: 0 0 auto;
	}
	.back {
		display: inline-flex;
		flex: 0 0 auto;
		gap: 4px;
		align-items: center;
		font-size: var(--nebu-text-body);
		color: var(--nebu-text-muted);
		text-decoration: none;
	}
	.back:hover {
		color: var(--neo-color-primary);
	}
	.slash {
		color: var(--nebu-border-strong);
	}
	.global {
		display: flex;
		flex: 0 0 auto;
		gap: var(--nebu-space-1);
		align-items: center;
		padding-left: var(--nebu-space-3);
		border-left: 1px solid var(--nebu-border);
	}
	.mobile .global {
		padding-left: 0;
		border-left: 0;
	}
	.tabs {
		display: flex;
		gap: var(--nebu-space-5);
		align-items: stretch;
		min-width: 0;
		height: 40px;
		margin-top: -6px;
		overflow-x: auto;
		overscroll-behavior-x: contain;
		scrollbar-width: none;
	}
	.tabs::-webkit-scrollbar {
		display: none;
	}
	.tabs.before {
		mask-image: linear-gradient(to right, transparent, #000 var(--nebu-space-6));
	}
	.tabs.after {
		mask-image: linear-gradient(to left, transparent, #000 var(--nebu-space-6));
	}
	.tabs.before.after {
		mask-image: linear-gradient(to right, transparent, #000 var(--nebu-space-6), #000 calc(100% - var(--nebu-space-6)), transparent);
	}
	.tab {
		display: inline-flex;
		flex: 0 0 auto;
		align-items: center;
		color: var(--nebu-text-muted);
		white-space: nowrap;
		text-decoration: none;
		border-bottom: 2px solid transparent;
		animation: nebu-reveal 220ms var(--nebu-settle) backwards;
		animation-delay: calc(var(--i) * 30ms);
		transition:
			color var(--nebu-fast) var(--nebu-ease),
			border-color var(--nebu-fast) var(--nebu-ease);
	}
	.tab:hover {
		color: var(--neo-text-color);
	}
	.tab.on {
		font-weight: 500;
		color: var(--neo-color-primary);
		border-bottom-color: var(--neo-color-primary);
	}
	/* A narrow bar grows with a long title, which takes two lines before it clips */
	.mobile .titlerow {
		gap: var(--nebu-space-2);
		height: auto;
		min-height: 52px;
		padding: var(--nebu-space-1) 0;
	}
	.mobile .title {
		display: -webkit-box;
		font-size: 17px;
		line-height: 22px;
		white-space: normal;
		overflow-wrap: anywhere;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
	}
	.mobile .back {
		justify-content: center;
		width: 40px;
		height: 40px;
		margin-left: -10px;
		color: var(--neo-text-color);
		border-radius: var(--neo-border-radius);
	}
</style>
