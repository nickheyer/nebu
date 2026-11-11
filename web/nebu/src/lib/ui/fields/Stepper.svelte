<script lang="ts">
	import type { Snippet } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { NeoInput } from '#lib/neo/inputs/index.js';
	import MinusIcon from 'phosphor-svelte/lib/MinusIcon';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';

	// The box of every number field, stepped by the arrow keys and by its buttons when it shows them
	let {
		id,
		raw = $bindable(''),
		focused = $bindable(false),
		min,
		max,
		step = 1,
		fallback,
		disabled = false,
		message,
		error,
		unit,
		ariaLabel,
		tail,
		buttons = true,
		onchange
	}: {
		id?: string;
		raw?: number | string | null;
		focused?: boolean;
		min?: number;
		max?: number;
		step?: number;
		fallback?: number;
		disabled?: boolean;
		message?: string;
		error?: string;
		unit?: string;
		ariaLabel?: string;
		tail?: Snippet;
		buttons?: boolean;
		onchange?: () => void;
	} = $props();

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

	// Steps from the typed number, an empty box starting at the number it resolves to
	function bump(up: boolean) {
		if (disabled) return;
		const base = parsed !== undefined && Number.isFinite(parsed) ? parsed : (fallback ?? min ?? 0);
		raw = String(clamp(base + (up ? step : -step)));
		onchange?.();
	}

	function onkeydown(e: KeyboardEvent) {
		if (e.key !== 'ArrowUp' && e.key !== 'ArrowDown') return;
		e.preventDefault();
		bump(e.key === 'ArrowUp');
	}
</script>

{#snippet after()}
	<span class="controls">
		{#if unit}<span class="unit">{unit}</span>{/if}
		{#if buttons}
			<NeoButton text aria-label="Decrease" title="Decrease" {disabled} onclick={() => bump(false)}>{#snippet icon()}<MinusIcon size={16} />{/snippet}</NeoButton>
			<NeoButton text aria-label="Increase" title="Increase" {disabled} onclick={() => bump(true)}>{#snippet icon()}<PlusIcon size={16} />{/snippet}</NeoButton>
		{/if}
		{#if tail}<span class="tail">{@render tail()}</span>{/if}
	</span>
{/snippet}

<NeoInput
	bind:value={raw}
	bind:focused
	type="text"
	inputmode={decimals > 0 ? 'decimal' : 'numeric'}
	{id}
	placeholder={fallback === undefined ? '' : String(fallback)}
	{disabled}
	{message}
	error={shownError}
	after={buttons || unit || tail ? after : undefined}
	elevation={-1}
	floating={false}
	width="100%"
	aria-label={ariaLabel}
	aria-invalid={shownError ? true : undefined}
	afterProps={{ class: 'stepper-after' }}
	class="stepper-input"
	oninput={typed}
	onblur={settle}
	{onkeydown}
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
