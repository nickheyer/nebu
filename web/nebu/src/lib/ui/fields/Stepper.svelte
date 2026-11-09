<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoInput } from '#lib/neo/inputs/index.js';
	import Tip from '#lib/ui/Tip.svelte';
	import { OffIcon, type Off } from '#lib/ui/fields/types.js';
	import MinusIcon from 'phosphor-svelte/lib/MinusIcon';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';

	// The box of every number field, digits only, the step buttons, the off toggle and any unit buttons in one group
	let {
		id,
		raw = $bindable(''),
		focused = $bindable(false),
		min,
		max,
		step = 1,
		placeholder = '',
		disabled = false,
		message,
		error,
		unit,
		off,
		isOff = false,
		ariaLabel,
		tail,
		onchange
	}: {
		id?: string;
		raw?: number | string | null;
		focused?: boolean;
		min?: number;
		max?: number;
		step?: number;
		placeholder?: string;
		disabled?: boolean;
		message?: string;
		error?: string;
		unit?: string;
		off?: Off;
		isOff?: boolean;
		ariaLabel?: string;
		tail?: Snippet;
		onchange?: () => void;
	} = $props();

	let ref = $state<HTMLInputElement>();
	const decimals = $derived(Math.max(0, (String(step).split('.')[1] ?? '').length));
	const negative = $derived(min === undefined || min < 0);
	const text = $derived(raw === null || raw === undefined ? '' : String(raw));
	const parsed = $derived(text === '' ? undefined : Number(text));
	// Text that is a number the bounds allow, or nothing at all
	const valid = $derived(parsed === undefined || (Number.isFinite(parsed) && (min === undefined || parsed >= min) && (max === undefined || parsed <= max)));
	const outOfRange = $derived.by(() => {
		if (parsed === undefined || !Number.isFinite(parsed)) return '';
		if (min !== undefined && parsed < min) return `At least ${min}`;
		if (max !== undefined && parsed > max) return `At most ${max}`;
		return '';
	});
	const shownError = $derived(error ?? (valid ? undefined : outOfRange || 'Not a number'));

	// Keeps digits, one leading minus where negatives are allowed and one point where steps are fractional
	function sanitize(s: string): string {
		let out = '';
		let point = false;
		for (const ch of s) {
			if (ch >= '0' && ch <= '9') out += ch;
			else if (ch === '-' && negative && out === '') out += ch;
			else if ((ch === '.' || ch === ',') && decimals > 0 && !point) {
				out += '.';
				point = true;
			}
		}
		return out;
	}

	function clamp(n: number): number {
		let v = n;
		if (min !== undefined) v = Math.max(min, v);
		if (max !== undefined) v = Math.min(max, v);
		return Number(v.toFixed(decimals));
	}

	function typed() {
		const clean = sanitize(text);
		if (clean !== text) raw = clean;
		onchange?.();
	}

	// Settles the box on leaving it, clamping a number past its bounds and dropping text that is none
	function settle() {
		if (text === '') return;
		const n = Number(text);
		if (!Number.isFinite(n)) {
			raw = '';
		} else {
			raw = String(clamp(n));
		}
		onchange?.();
	}

	function bump(up: boolean) {
		if (disabled) return;
		const base = parsed !== undefined && Number.isFinite(parsed) ? parsed : Number(placeholder) || min || 0;
		raw = String(clamp(base + (up ? step : -step)));
		onchange?.();
	}

	// Empties the box, or fills an empty one with the placeholder number or the first step
	function toggle() {
		if (disabled) return;
		if (isOff) {
			const hinted = Number(placeholder);
			raw = String(Number.isFinite(hinted) && hinted > 0 ? hinted : Math.max(min ?? 0, step) || 1);
			ref?.focus();
		} else {
			raw = '';
		}
		onchange?.();
	}
</script>

{#snippet after()}
	<span class="controls">
		{#if unit}<span class="unit">{unit}</span>{/if}
		<NeoButton text aria-label="Decrease" title="Decrease" {disabled} onclick={() => bump(false)}>{#snippet icon()}<MinusIcon size={16} />{/snippet}</NeoButton>
		<NeoButton text aria-label="Increase" title="Increase" {disabled} onclick={() => bump(true)}>{#snippet icon()}<PlusIcon size={16} />{/snippet}</NeoButton>
		{#if off}
			<Tip text={off.label} tag="span">
				<NeoButton text toggle checked={isOff} color={isOff ? 'primary' : undefined} aria-label={off.label} aria-pressed={isOff} {disabled} onclick={toggle}>{#snippet icon()}<OffIcon size={16} />{/snippet}</NeoButton>
			</Tip>
		{/if}
		{#if tail}<span class="tail">{@render tail()}</span>{/if}
	</span>
{/snippet}

<NeoInput
	bind:ref
	bind:value={raw}
	bind:focused
	type="text"
	inputmode={decimals > 0 ? 'decimal' : 'numeric'}
	{id}
	{placeholder}
	{disabled}
	{message}
	error={shownError}
	{after}
	elevation={-1}
	floating={false}
	width="100%"
	aria-label={ariaLabel}
	aria-invalid={shownError ? true : undefined}
	afterProps={{ class: 'stepper-after' }}
	class="stepper-input"
	oninput={typed}
	onblur={settle}
/>

<style>
	:global(.neo-input-after.stepper-after) {
		padding: 0 4px 0 0;
	}
	:global(.neo-input-group:has(.stepper-input)) {
		flex: 1 1 auto;
	}
	.controls {
		display: inline-flex;
		gap: 2px;
		align-items: center;
	}
	.controls :global(.neo-button) {
		--neo-btn-min-width: 2rem;
		--neo-btn-padding: 0.375rem 0.5rem;
		--neo-btn-padding-empty: 0.375rem;
	}
	.unit {
		padding: 0 8px 0 4px;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		white-space: nowrap;
	}
	.tail {
		display: inline-flex;
		gap: 0;
		align-items: center;
		margin-left: 2px;
		padding-left: 4px;
		border-left: 1px solid var(--nebu-border);
	}
	.tail :global(.neo-button) {
		--neo-btn-min-width: 1.5rem;
		--neo-btn-padding: 0.25rem 0.25rem;

		font-size: var(--nebu-text-secondary);
	}
</style>
