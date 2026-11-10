<script lang="ts" module>
	// Pixels one character of row text takes
	const chPx = 8.5;

	export function toPx(w: string): string {
		return w.endsWith('ch') ? `${Math.round(Number(w.slice(0, -2)) * chPx)}px` : w;
	}
</script>

<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import Dot from '#lib/ui/Dot.svelte';
	import ListScope from '#lib/ui/ListScope.svelte';
	import Menu, { type MenuItem } from '#lib/ui/Menu.svelte';
	import Str, { type StrKind } from '#lib/ui/Str.svelte';
	import Tip from '#lib/ui/Tip.svelte';
	import { compactBelow, useList, weightOf, type ListSlot } from '#lib/ui/list.svelte.js';
	import type { DotShape, Lamp } from '#lib/ui/status.js';
	import CaretDownIcon from 'phosphor-svelte/lib/CaretDownIcon';

	export type Fact = { value: string; tip?: string; mono?: boolean };

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
		instrument?: Snippet;
		below?: Snippet;
		href?: string;
		onclick?: () => void;
		selected?: boolean;
	} = $props();

	const list = useList();

	let open = $state(false);
	let width = $state(0);
	const compact = $derived(list ? list.compact : width > 0 && width < compactBelow);
	const shownFacts = $derived(list ? facts.slice(0, 2) : facts.filter((f) => f.value !== '').slice(0, 2));
	const dropped = $derived(compact && (secondary !== undefined || facts.slice(1).some((f) => f.value !== '')));
	const expandable = $derived(expand !== undefined || dropped);
	const clickable = $derived(href !== undefined || onclick !== undefined);
	const primaryWidthCh = $derived(primaryWidth ?? (primaryKind === 'repo' || primaryKind === 'path' ? '40ch' : primaryKind === 'message' ? '64ch' : '24ch'));
	const primaryBasis = $derived(toPx(primaryWidthCh));
	const secondaryBasis = $derived(toPx(secondaryWidth));

	$effect(() => {
		if (!list) return;
		const slots: ListSlot[] = [];
		if (leading) slots.push('leading');
		if (lamp) slots.push('dot');
		if (secondary !== undefined) slots.push('secondary');
		if (instrument) slots.push('instrument');
		if (action) slots.push('action');
		if (menu?.length) slots.push('menu');
		if (expandable) slots.push('caret');
		list.primaryWeight = weightOf(primaryWidthCh, 1);
		list.secondaryWeight = weightOf(secondaryWidth, 1.4);
		return list.use(slots, Math.min(2, facts.length));
	});

	function inControl(target: EventTarget | null): boolean {
		return (target as HTMLElement | null)?.closest('button, a, input, [role=menu], [role=menuitem]') !== null;
	}

	function activate(e: MouseEvent) {
		if (inControl(e.target)) return;
		onclick?.();
	}

	function keyActivate(e: KeyboardEvent) {
		if (inControl(e.target) && e.target !== e.currentTarget) return;
		if (e.key === 'Enter' || e.key === ' ') {
			e.preventDefault();
			onclick?.();
		}
	}
</script>

{#snippet name()}
	{#if href}
		<a {href} class="link"><Str value={primary} kind={primaryKind} tip={primaryTip} weight={400} width="100%" /></a>
	{:else}
		<Str value={primary} kind={primaryKind} tip={primaryTip} weight={400} width="100%" />
	{/if}
	{#if badges}<span class="badges">{@render badges()}</span>{/if}
{/snippet}

{#snippet fact(f: Fact | undefined)}
	{#if f && f.value !== ''}
		<Tip text={f.tip} tag="span" class="facttip">
			<span class="fact" class:mono={f.mono}>{f.value}</span>
		</Tip>
	{/if}
{/snippet}

{#snippet caret()}
	<NeoButton text aria-label={open ? 'Collapse' : 'Expand'} aria-expanded={open} onclick={() => (open = !open)}>
		{#snippet icon()}<span class="caret" class:open><CaretDownIcon size={16} /></span>{/snippet}
	</NeoButton>
{/snippet}

{#snippet more()}
	<ListScope>
		{#if instrument && compact}<div class="below">{@render instrument()}</div>{/if}
		{#if below}<div class="below">{@render below()}</div>{/if}
		{#if open && expandable}
			<div class="expanded">
				{#if dropped}
					<div class="dropped">
						{#if secondary !== undefined}<Str value={secondary} kind={secondaryKind} tip={secondaryTip} color={secondaryColor ?? 'muted'} width="100%" />{/if}
						{#each facts.slice(1).filter((f) => f.value !== '') as f, i (i)}<span class="fact" class:mono={f.mono}>{f.value}</span>{/each}
					</div>
				{/if}
				{@render expand?.()}
			</div>
		{/if}
	</ListScope>
{/snippet}

{#if list}
	<div class="rowwrap grid" class:selected>
		<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
		<div class="listrow" class:clickable role={clickable && !href ? 'button' : undefined} tabindex={clickable && !href ? 0 : undefined} aria-pressed={clickable && !href && selected ? true : undefined} onclick={activate} onkeydown={clickable && !href ? keyActivate : undefined}>
			{#if list.has('leading')}<span class="cell">{#if leading}{@render leading()}{/if}</span>{/if}
			{#if list.has('dot')}<span class="cell">{#if lamp}<Dot {lamp} {shape} />{/if}</span>{/if}
			<span class="cell primary">{@render name()}</span>
			{#if list.has('secondary')}
				<span class="cell secondary-col">{#if secondary !== undefined}<Str value={secondary} kind={secondaryKind} tip={secondaryTip} color={secondaryColor ?? 'muted'} width="100%" />{/if}</span>
			{/if}
			{#if list.has('instrument')}<span class="cell instrument">{#if instrument}{@render instrument()}{/if}</span>{/if}
			{#each { length: list.shownFacts } as _, i (i)}<span class="cell end">{@render fact(shownFacts[i])}</span>{/each}
			{#if list.has('action')}<span class="cell end action">{#if action}{@render action()}{/if}</span>{/if}
			{#if list.has('menu')}<span class="cell">{#if menu?.length}<Menu items={menu} />{/if}</span>{/if}
			{#if list.has('caret')}<span class="cell">{#if expandable}{@render caret()}{/if}</span>{/if}
		</div>
		{@render more()}
	</div>
{:else}
	<div class="rowwrap" class:selected bind:clientWidth={width}>
		<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
		<div class="listrow flex" class:clickable role={clickable && !href ? 'button' : undefined} tabindex={clickable && !href ? 0 : undefined} aria-pressed={clickable && !href && selected ? true : undefined} onclick={activate} onkeydown={clickable && !href ? keyActivate : undefined}>
			{#if leading}<span class="leading">{@render leading()}</span>{/if}
			{#if lamp}<Dot {lamp} {shape} />{/if}
			<span class="primary" style:flex-basis={compact ? 'auto' : primaryBasis} style:flex-grow={instrument ? 0 : 1}>
				{@render name()}
			</span>
			{#if !compact && secondary !== undefined}
				<span class="secondary-col flexsec" style:flex-basis={secondaryBasis}>
					<Str value={secondary} kind={secondaryKind} tip={secondaryTip} color={secondaryColor ?? 'muted'} width="100%" />
				</span>
			{/if}
			{#if instrument && !compact}
				<span class="instrument">{@render instrument()}</span>
			{:else}
				<span class="fill"></span>
			{/if}
			{#each compact ? shownFacts.slice(0, 1) : shownFacts as f, i (i)}{@render fact(f)}{/each}
			{#if action}<span class="action">{@render action()}</span>{/if}
			{#if menu?.length}<Menu items={menu} />{/if}
			{#if expandable}{@render caret()}{/if}
		</div>
		{@render more()}
	</div>
{/if}

<style>
	.rowwrap {
		position: relative;
		min-width: 0;
		border-bottom: 1px solid var(--nebu-border);
	}
	.rowwrap:last-child {
		border-bottom-color: transparent;
	}
	.rowwrap.grid {
		display: grid;
		grid-template-columns: subgrid;
		grid-column: 1 / -1;
	}
	.rowwrap.grid > :global(*) {
		grid-column: 1 / -1;
	}
	.rowwrap.grid > .listrow {
		display: grid;
		grid-template-columns: subgrid;
		align-items: center;
		padding: var(--nebu-space-1) var(--nebu-row-inset, var(--nebu-space-2));
	}
	.rowwrap.selected {
		background: color-mix(in srgb, var(--neo-color-primary) 9%, transparent);
	}
	.rowwrap.selected::before {
		position: absolute;
		top: 6px;
		bottom: 6px;
		left: 0;
		width: 3px;
		content: '';
		background: var(--neo-color-primary);
		border-radius: 0 3px 3px 0;
	}
	.listrow {
		min-height: 44px;
		transition: background var(--nebu-normal) var(--nebu-ease);
	}
	.listrow.flex {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		padding: var(--nebu-space-1) var(--nebu-row-inset, var(--nebu-space-2));
	}
	.listrow.clickable {
		cursor: pointer;
	}
	.listrow.clickable:hover {
		background: var(--neo-background-color-hover);
	}
	.listrow.clickable:focus-visible {
		outline-offset: -2px;
	}
	.cell {
		display: flex;
		align-items: center;
		min-width: 0;
	}
	.cell.end {
		justify-content: flex-end;
	}
	.leading {
		display: inline-flex;
		flex: 0 0 auto;
		gap: 8px;
		align-items: center;
	}
	/* The name and its badges share a line while they fit and stack when the row is tight */
	.primary {
		display: inline-flex;
		flex-wrap: wrap;
		gap: 2px 6px;
		align-items: center;
		min-width: 0;
	}
	.listrow.flex .primary {
		flex: 1 1 auto;
		min-width: 12ch;
	}
	.primary > :global(.tip),
	.primary > .link {
		flex: 0 1 auto;
		min-width: 0;
	}
	.badges {
		display: inline-flex;
		flex: 0 1 auto;
		flex-wrap: wrap;
		gap: 4px;
		align-items: center;
		min-width: 0;
	}
	.secondary-col {
		font-size: var(--nebu-text-secondary);
	}
	.flexsec {
		display: inline-flex;
		flex: 0 2 auto;
		min-width: 8ch;
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
		align-items: center;
		min-width: 0;
	}
	.listrow.flex .instrument {
		flex: 1 1 0;
		min-width: 12rem;
	}
	.fact {
		display: inline-block;
		font-variant-numeric: tabular-nums;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		text-align: right;
		white-space: nowrap;
	}
	.listrow.flex .fact {
		flex: 0 0 auto;
		min-width: 8ch;
	}
	.fact.mono {
		font-family: var(--nebu-font-mono);
	}
	:global(.tip.facttip) {
		flex: 0 0 auto;
	}
	.action {
		display: inline-flex;
		flex: 0 0 auto;
		gap: 4px;
		align-items: center;
	}
	.caret {
		display: inline-flex;
		color: var(--nebu-text-muted);
		transition: transform var(--nebu-normal) var(--nebu-settle);
	}
	.caret.open {
		transform: rotate(180deg);
	}
	.below {
		display: flex;
		min-width: 0;
		padding: 0 var(--nebu-row-inset, var(--nebu-space-2)) var(--nebu-space-2);
	}
	.expanded {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-2);
		padding: var(--nebu-space-1) var(--nebu-row-inset, var(--nebu-space-2)) var(--nebu-space-3) calc(var(--nebu-row-inset, var(--nebu-space-2)) + 20px);
		animation: nebu-reveal 240ms var(--nebu-settle) backwards;
	}
	.dropped {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-1) var(--nebu-space-4);
		font-size: var(--nebu-text-secondary);
	}
	.dropped .fact {
		text-align: left;
	}
</style>
