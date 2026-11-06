<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import Dot from '#lib/ui/Dot.svelte';
	import Menu, { type MenuItem } from '#lib/ui/Menu.svelte';
	import Str, { type StrKind } from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import type { DotShape, Lamp } from '#lib/ui/status.js';
	import CaretDownIcon from 'phosphor-svelte/lib/CaretDownIcon';

	export type Fact = { value: string; tip?: string; mono?: boolean };

	// Pixels one character of the row's text takes, so widths given in ch become flex bases
	const chPx = 8.5;
	// The row width under which the secondary phrase and the facts move into the expand
	const compactBelow = 640;

	let {
		lamp,
		shape = 'filled',
		primary,
		primaryKind = 'name',
		primaryTip,
		primaryWidth,
		secondary,
		secondaryKind = 'text',
		secondaryTip,
		secondaryWidth = '40ch',
		secondaryColor,
		facts = [],
		action,
		menu,
		expand,
		leading,
		badges,
		instrument,
		below,
		href,
		onclick,
		selected = false
	}: {
		lamp?: Lamp;
		shape?: DotShape;
		primary: string;
		primaryKind?: StrKind;
		primaryTip?: string;
		primaryWidth?: string;
		secondary?: string;
		secondaryKind?: StrKind;
		secondaryTip?: string;
		secondaryWidth?: string;
		secondaryColor?: 'muted' | 'error' | 'warning' | 'success';
		facts?: Fact[];
		action?: Snippet;
		menu?: MenuItem[];
		expand?: Snippet;
		leading?: Snippet;
		badges?: Snippet;
		// A bar between the phrase and the facts, spanning its own line under the row on mobile
		instrument?: Snippet;
		// A line under the row spanning its width
		below?: Snippet;
		href?: string;
		onclick?: () => void;
		selected?: boolean;
	} = $props();

	let open = $state(false);
	let width = $state(0);
	const compact = $derived(width > 0 && width < compactBelow);
	const shownFacts = $derived(facts.filter((f) => f.value !== '').slice(0, 2));
	const dropped = $derived(compact && (secondary !== undefined || shownFacts.length > 0));
	const expandable = $derived(expand !== undefined || dropped);
	const clickable = $derived(href !== undefined || onclick !== undefined);
	const primaryBasis = $derived(toPx(primaryWidth ?? (primaryKind === 'repo' || primaryKind === 'path' ? '40ch' : primaryKind === 'message' ? '64ch' : '24ch')));
	const secondaryBasis = $derived(toPx(secondaryWidth));

	function toPx(w: string): string {
		return w.endsWith('ch') ? `${Math.round(Number(w.slice(0, -2)) * chPx)}px` : w;
	}

	function activate(e: MouseEvent) {
		if ((e.target as HTMLElement).closest('button, a, input, [role=menu]')) return;
		onclick?.();
	}
</script>

<div class="rowwrap" class:selected bind:clientWidth={width}>
	<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
	<div class="listrow" class:clickable role={clickable && !href ? 'button' : undefined} tabindex={clickable && !href ? 0 : undefined} onclick={activate} onkeydown={(e) => e.key === 'Enter' && onclick?.()}>
		{#if leading}<span class="leading">{@render leading()}</span>{/if}
		{#if lamp}<Dot {lamp} {shape} />{/if}
		<span class="primary" style:flex-basis={compact ? 'auto' : primaryBasis}>
			{#if href}
				<a {href} class="link"><Str value={primary} kind={primaryKind} tip={primaryTip} weight={500} width="100%" /></a>
			{:else}
				<Str value={primary} kind={primaryKind} tip={primaryTip} weight={500} width="100%" />
			{/if}
			{#if badges}<span class="badges">{@render badges()}</span>{/if}
		</span>
		{#if !compact && secondary !== undefined}
			<span class="secondary-col" style:flex-basis={secondaryBasis}>
				<Str value={secondary} kind={secondaryKind} tip={secondaryTip} color={secondaryColor ?? 'muted'} width="100%" />
			</span>
		{/if}
		{#if instrument && !compact}
			<span class="instrument">{@render instrument()}</span>
		{:else}
			<span class="fill"></span>
		{/if}
		{#if !compact}
			{#each shownFacts as f, i (i)}
				<Tip text={f.tip} tag="span">
					<span class="fact" class:mono={f.mono}>{f.value}</span>
				</Tip>
			{/each}
		{/if}
		{#if action}<span class="action">{@render action()}</span>{/if}
		{#if menu?.length}<Menu items={menu} />{/if}
		{#if expandable}
			<NeoButton text aria-label={open ? 'Collapse' : 'Expand'} aria-expanded={open} onclick={() => (open = !open)}>
				{#snippet icon()}<span class="caret" class:open><CaretDownIcon size={16} /></span>{/snippet}
			</NeoButton>
		{/if}
	</div>
	{#if instrument && compact}<div class="below">{@render instrument()}</div>{/if}
	{#if below}<div class="below">{@render below()}</div>{/if}
	{#if open && expandable}
		<div class="expanded">
			{#if dropped}
				<div class="dropped">
					{#if secondary !== undefined}<Str value={secondary} kind={secondaryKind} tip={secondaryTip} color={secondaryColor ?? 'muted'} width="100%" />{/if}
					{#each facts.filter((f) => f.value !== '') as f, i (i)}<span class="fact" class:mono={f.mono}>{f.value}</span>{/each}
				</div>
			{/if}
			{@render expand?.()}
		</div>
	{/if}
</div>

<style>
	.rowwrap {
		min-width: 0;
		border-bottom: 1px solid var(--nebu-border);
	}
	.rowwrap.selected {
		background: color-mix(in srgb, var(--neo-color-primary) 8%, transparent);
	}
	.listrow {
		display: flex;
		gap: 12px;
		align-items: center;
		min-height: 44px;
		padding: 4px 8px;
		border-radius: 8px;
		transition: background 150ms ease-out;
	}
	.listrow.clickable {
		cursor: pointer;
	}
	.listrow:hover {
		background: var(--neo-background-color-hover);
	}
	.leading {
		display: inline-flex;
		flex: 0 0 auto;
		gap: 8px;
		align-items: center;
	}
	.primary {
		display: inline-flex;
		flex: 1 1 auto;
		gap: 6px;
		align-items: center;
		min-width: 12ch;
		max-width: 100%;
	}
	.primary > :global(.tip),
	.primary > .link {
		flex: 0 1 auto;
		min-width: 0;
	}
	.badges {
		display: inline-flex;
		flex: 0 0 auto;
		gap: 4px;
		align-items: center;
	}
	.secondary-col {
		display: inline-flex;
		flex: 0 2 auto;
		min-width: 8ch;
		font-size: 13px;
	}
	.link {
		max-width: 100%;
		color: inherit;
		text-decoration: none;
	}
	.link:hover {
		color: var(--neo-color-primary);
	}
	.fill {
		flex: 1 1 0;
	}
	.instrument {
		display: flex;
		flex: 1 1 0;
		align-items: center;
		min-width: 6rem;
	}
	.fact {
		display: inline-block;
		flex: 0 0 auto;
		min-width: 8ch;
		font-variant-numeric: tabular-nums;
		font-size: 13px;
		color: var(--nebu-text-muted);
		text-align: right;
		white-space: nowrap;
	}
	.fact.mono {
		font-family: var(--nebu-font-mono);
	}
	.action {
		display: inline-flex;
		flex: 0 0 auto;
		gap: 4px;
		align-items: center;
	}
	.caret {
		display: inline-flex;
		transition: transform 150ms ease-out;
	}
	.caret.open {
		transform: rotate(180deg);
	}
	.below {
		display: flex;
		min-width: 0;
		padding: 0 8px 8px;
	}
	.expanded {
		display: flex;
		flex-direction: column;
		gap: 8px;
		padding: 4px 8px 12px 28px;
	}
	.dropped {
		display: flex;
		flex-wrap: wrap;
		gap: 4px 16px;
		font-size: 13px;
	}
	.dropped .fact {
		text-align: left;
	}
	@media (prefers-reduced-motion: reduce) {
		.caret,
		.listrow {
			transition: none;
		}
	}
</style>
