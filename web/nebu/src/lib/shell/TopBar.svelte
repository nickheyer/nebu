<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { NeoBadge } from '#lib/neo/badge/index.js';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { live } from '#lib/live/events.svelte.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import AccountMenu from '#lib/shell/AccountMenu.svelte';
	import Brand from '#lib/shell/Brand.svelte';
	import { chrome, type HeaderTab } from '#lib/shell/chrome.svelte.js';
	import { pageOf } from '#lib/shell/nav.js';
	import Dot from '#lib/ui/Dot.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import CaretLeftIcon from 'phosphor-svelte/lib/CaretLeftIcon';
	import ListChecksIcon from 'phosphor-svelte/lib/ListChecksIcon';

	const head = $derived(chrome.header);
	const title = $derived(head?.title ?? pageOf(page.url.pathname)?.label ?? '');
	const tabs = $derived(head?.tabs ?? []);
	const active = $derived(live.activeTasks.length);
	const liveState = $derived(!session.reads ? 'off' : live.connected ? 'live' : live.error ? 'reconnecting' : 'connecting');
	const liveWord = $derived(liveState === 'live' ? 'Live' : liveState === 'reconnecting' ? 'Reconnecting' : 'Connecting');
	const liveTip = $derived(liveState === 'reconnecting' && live.error ? `Reconnecting, ${live.error}` : liveWord);
	const liveLamp = $derived(liveState === 'live' ? 'success' : liveState === 'reconnecting' ? 'error' : liveState === 'connecting' ? 'warning' : 'muted');
	const liveShape = $derived(liveState === 'live' ? 'filled' : liveState === 'connecting' ? 'half' : 'hollow');

	// The bar's height, read by panels that stick under it
	let height = $state(0);
	$effect(() => {
		document.documentElement.style.setProperty('--nebu-top', `${height}px`);
	});

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

<header class="top" class:mobile={viewport.mobile} bind:clientHeight={height}>
	<div class="frame">
		<div class="titlerow">
			{#if viewport.mobile}<Brand collapsed />{/if}
			{#if head?.back}
				<a class="back" href={head.back.href}><CaretLeftIcon size={16} /><span>{head.back.label}</span></a>
				<span class="slash" aria-hidden="true">/</span>
			{/if}
			<h1 class="title ellipsis">{title}</h1>
			{#if head?.meta && !viewport.mobile}<div class="meta">{@render head.meta()}</div>{/if}
			<span class="fill"></span>
			{#if head?.actions && !viewport.mobile}<div class="actions">{@render head.actions()}</div>{/if}
			<div class="global">
				{#if session.reads}
					{#if active > 0}
						<NeoBadge value={String(active)} placement="top-right" size="small" color="primary">
							{@render tasks()}
						</NeoBadge>
					{:else}
						{@render tasks()}
					{/if}
					<Tip text={liveTip} tag="span">
						<span class="live" role="status"><Dot lamp={liveLamp} shape={liveShape} label={viewport.mobile ? liveWord : undefined} text={viewport.mobile ? undefined : liveWord} /></span>
					</Tip>
				{/if}
				<AccountMenu />
			</div>
		</div>
		{#if viewport.mobile && (head?.meta || head?.actions)}
			<div class="subrow">
				{#if head?.meta}<div class="meta">{@render head.meta()}</div>{/if}
				<span class="fill"></span>
				{#if head?.actions}<div class="actions">{@render head.actions()}</div>{/if}
			</div>
		{/if}
		{#if tabs.length}
			{#if viewport.mobile && tabs.length > 3}
				<div class="tabselect">
					<Select value={head?.active} options={tabs.map((t) => ({ value: t.id, label: t.count !== undefined ? `${t.label} ${t.count}` : t.label }))} onchange={(v) => { const t = tabs.find((x) => x.id === v); if (t) pick(t); }} />
				</div>
			{:else}
				<nav class="tabs" aria-label="Tabs">
					{#each tabs as t (t.id)}
						{@const on = t.id === head?.active}
						<a
							class="tab"
							class:on
							href={t.href}
							aria-current={on ? 'page' : undefined}
							onclick={(e) => {
								if (head?.ontab || !t.href) {
									e.preventDefault();
									pick(t);
								}
							}}
						>
							{t.label}{#if t.count !== undefined}<span class="count">{t.count}</span>{/if}
						</a>
					{/each}
				</nav>
			{/if}
		{/if}
	</div>
</header>

<style>
	/* One bar over every page, its title, tabs and actions left and the shell lamps right */
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
	.title {
		flex: 0 1 auto;
		min-width: 0;
		font-size: var(--nebu-text-title);
	}
	.back {
		display: inline-flex;
		flex: 0 0 auto;
		gap: 4px;
		align-items: center;
		font-size: var(--nebu-text-body);
		font-weight: 500;
		color: var(--nebu-text-muted);
		text-decoration: none;
	}
	.back:hover {
		color: var(--neo-color-primary);
	}
	.slash {
		color: var(--nebu-border-strong);
	}
	.meta {
		display: inline-flex;
		flex: 0 1 auto;
		gap: var(--nebu-space-2) var(--nebu-space-3);
		align-items: center;
		min-width: 0;
		overflow: hidden;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
	.actions {
		display: flex;
		flex: 0 0 auto;
		gap: var(--nebu-space-2);
		align-items: center;
	}
	/* A phone gives the meta and actions their own line so the title keeps its width */
	.subrow {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-2) var(--nebu-space-3);
		align-items: center;
		min-width: 0;
		padding-bottom: var(--nebu-space-3);
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
	.live {
		display: inline-flex;
		padding: 0 var(--nebu-space-2);
		font-size: var(--nebu-text-secondary);
	}
	.tabs {
		display: flex;
		gap: var(--nebu-space-5);
		align-items: stretch;
		min-width: 0;
		height: 40px;
		margin-top: -6px;
	}
	.tab {
		display: inline-flex;
		gap: 6px;
		align-items: center;
		font-weight: 500;
		color: var(--nebu-text-muted);
		white-space: nowrap;
		text-decoration: none;
		border-bottom: 2px solid transparent;
		transition:
			color var(--nebu-fast) var(--nebu-ease),
			border-color var(--nebu-fast) var(--nebu-ease);
	}
	.tab:hover {
		color: var(--neo-text-color);
	}
	.tab.on {
		color: var(--neo-color-primary);
		border-bottom-color: var(--neo-color-primary);
	}
	.count {
		font-size: var(--nebu-text-caption);
		font-variant-numeric: tabular-nums;
		color: var(--nebu-text-muted);
	}
	.tabselect {
		padding-bottom: var(--nebu-space-3);
	}
	.mobile .titlerow {
		height: 52px;
	}
</style>
