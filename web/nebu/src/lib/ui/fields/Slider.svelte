<script lang="ts">
	import { tick } from 'svelte';
	import { num } from '#lib/format.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import Resting from '#lib/ui/fields/Resting.svelte';
	import { decimalsOf, land, place, stride, ticks, type Curve, type Named, type Span } from '#lib/ui/fields/scale.js';
	import { fieldId } from '#lib/ui/fields/types.js';

	// A number dragged along a labelled rail or typed in the box beside its name, an unset one leaving the box empty over the line saying what it rests on
	let {
		value = $bindable(),
		min = 0,
		max = 100,
		step = 1,
		curve = 'linear',
		unit,
		label,
		info,
		disabled = false,
		message,
		// What an unset value comes to, a number drawn as a hollow knob at its place
		resting,
		// The words for what an unset value comes to, when the number or word alone does not say it
		restingText,
		restingWord,
		// Numbers shown by what they mean, one outside the rail taking the knob off it
		names = [],
		optional = false,
		// Typed numbers stop at the rail's ends, off for a rail that only suggests a range
		bounded = true,
		// A shorter rail and knob, for a narrow column of settings
		inline = false,
		onchange
	}: {
		value?: number;
		min?: number;
		max?: number;
		step?: number;
		curve?: Curve;
		unit?: string;
		label: string;
		info?: string;
		disabled?: boolean;
		message?: string;
		resting?: number | string;
		restingText?: string;
		restingWord?: string;
		names?: Named[];
		optional?: boolean;
		bounded?: boolean;
		inline?: boolean;
		onchange?: (n: number | undefined) => void;
	} = $props();

	const id = fieldId();
	const decimals = $derived(decimalsOf(step));
	const name = $derived(value === undefined ? undefined : names.find((n) => n.value === value));
	// A named number outside the rail has no place on it
	const off = $derived(name !== undefined && (name.value < min || name.value > max));
	// The rail's span, stretched to hold a number typed past its ends
	const span = $derived<Span>({ min: value === undefined || off ? min : Math.min(min, value), max: value === undefined || off ? max : Math.max(max, value), step, curve });
	// The number an unset value comes to, when it is one the rail holds
	const rest = $derived.by(() => {
		const n = typeof resting === 'number' ? resting : resting === undefined || resting.trim() === '' ? NaN : Number(resting);
		return Number.isFinite(n) && n >= min && n <= max ? n : undefined;
	});
	const unset = $derived(value === undefined);
	// A hollow knob marks where an unset value rests, no knob when it rests on no number
	const ghost = $derived(unset && rest !== undefined);
	const bare = $derived(off || (unset && rest === undefined && optional));
	const at = $derived(value !== undefined && !off ? place(value, span) : rest !== undefined ? place(rest, span) : 0);
	const reading = $derived(value === undefined ? '' : name ? name.label : write(value));
	// Stops few enough to mark each one on the rail
	const stops = $derived(curve === 'linear' && span.max > span.min && (span.max - span.min) / step <= 10 ? Math.round((span.max - span.min) / step) : 0);
	const size = $derived(inline ? 16 : 20);
	let width = $state(0);
	const scale = $derived(ticks(span, Math.max(0, width - size), 6.4));

	let rail = $state<HTMLElement>();
	let knob = $state<HTMLButtonElement>();
	let figure = $state<HTMLInputElement>();
	let held = $state(false);
	// Whether the knob took the keys from a pointer, which draws no focus ring
	let pointed = $state(false);
	let typing = $state(false);
	let draft = $state('');
	// The text the box opened with, so a box left as it was changes nothing
	let opened = '';
	// Pixels from the knob's centre to where the pointer took hold of it
	let grab = 0;
	let dropped = false;
	// Where a finger came down, kept until it moves along the rail far enough to mean a drag
	let touched: number | undefined;

	// A number as its reading shows it, whole ones grouped and the rest at the step's decimals
	function write(n: number): string {
		return decimals === 0 && Number.isInteger(n) ? num(n) : plain(n);
	}

	// A number as typed, digits alone
	function plain(n: number): string {
		return n.toFixed(Math.max(decimals, decimalsOf(n)));
	}

	function take(n: number | undefined) {
		if (disabled || n === value) return;
		value = n;
		onchange?.(n);
	}

	// The value under a pointer along the rail the knob travels
	function under(e: PointerEvent): number {
		const box = rail!.getBoundingClientRect();
		return land((e.clientX - box.left - grab - size / 2) / (box.width - size), { min, max, step, curve });
	}

	// Takes hold of the knob where it is, or brings it to a press anywhere else along the rail
	function press(e: PointerEvent) {
		if (disabled || e.button !== 0) return;
		e.preventDefault();
		const face = knob!.getBoundingClientRect();
		const from = e.clientX - (face.left + face.width / 2);
		const onKnob = !bare && Math.abs(from) <= face.width / 2 + 2;
		grab = onKnob ? from : 0;
		held = true;
		pointed = true;
		touched = e.pointerType === 'touch' ? e.clientX : undefined;
		rail!.setPointerCapture(e.pointerId);
		knob!.focus({ preventScroll: true });
		if (!onKnob && touched === undefined) take(under(e));
	}

	// Follows the pointer, a finger only once it has moved along the rail, so a scroll that starts here changes nothing
	function drag(e: PointerEvent) {
		if (!held) return;
		if (touched !== undefined && Math.abs(e.clientX - touched) < 6) return;
		touched = undefined;
		take(under(e));
	}

	// Lets go, a finger lifted where it came down bringing the knob to it
	function release(e: PointerEvent) {
		if (held && touched !== undefined) take(under(e));
		drop();
	}

	function drop() {
		held = false;
		touched = undefined;
	}

	// Steps from the value, one with no place on the rail first taking the number it rests on or the rail's start
	function bump(up: boolean, times: number) {
		if (value === undefined || off) return take(rest ?? min);
		const next = value + (up ? step : -step) * times;
		take(Number(Math.min(span.max, Math.max(span.min, next)).toFixed(Math.max(decimals, decimalsOf(value)))));
	}

	// Opens the box for typing, starting it with a character already pressed
	async function edit(first?: string) {
		typing = true;
		opened = value === undefined || name ? '' : plain(value);
		draft = first ?? opened;
		await tick();
		figure?.focus({ preventScroll: true });
		if (first === undefined) figure?.select();
		else figure?.setSelectionRange(draft.length, draft.length);
	}

	const negative = $derived(min < 0 || names.some((n) => n.value < 0));

	// Arrows step, page keys stride, home and end take the rail's ends, delete unsets, a digit starts typing
	function keyed(e: KeyboardEvent) {
		pointed = false;
		if (disabled || e.metaKey || e.ctrlKey || e.altKey) return;
		const k = e.key;
		const numeric = (k >= '0' && k <= '9') || (k === '-' && negative) || ((k === '.' || k === ',') && decimals > 0);
		if (k === 'ArrowRight' || k === 'ArrowUp') bump(true, e.shiftKey ? 10 : 1);
		else if (k === 'ArrowLeft' || k === 'ArrowDown') bump(false, e.shiftKey ? 10 : 1);
		else if (k === 'PageUp' || k === 'PageDown') take(stride(value === undefined || off ? (rest ?? min) : value, k === 'PageUp', { min, max, step, curve }));
		else if (k === 'Home') take(min);
		else if (k === 'End') take(max);
		else if ((k === 'Delete' || k === 'Backspace') && optional) take(undefined);
		else if (k === 'Enter' || k === 'F2') void edit();
		else if (numeric) void edit(k === ',' ? '.' : k);
		else return;
		e.preventDefault();
	}

	// Keeps digits, one leading minus where a number may go below zero and one point where steps are fractional
	function clean(s: string): string {
		let out = '';
		for (const ch of s) {
			if (ch >= '0' && ch <= '9') out += ch;
			else if (ch === '-' && negative && out === '') out += ch;
			else if ((ch === '.' || ch === ',') && decimals > 0 && !out.includes('.')) out += '.';
		}
		return out;
	}

	function typed(e: Event & { currentTarget: HTMLInputElement }) {
		const text = clean(e.currentTarget.value);
		if (text !== e.currentTarget.value) e.currentTarget.value = text;
		draft = text;
	}

	// Takes the typed number, held to the rail's ends when they bind, an emptied box unsetting an optional value
	function commit() {
		if (draft === opened) return;
		if (draft === '') {
			if (optional) take(undefined);
			return;
		}
		const n = Number(draft);
		if (!Number.isFinite(n)) return;
		take(bounded && !names.some((x) => x.value === n) ? Math.min(max, Math.max(min, n)) : n);
	}

	function left() {
		if (!dropped) commit();
		dropped = false;
		typing = false;
	}

	// Enter keeps the typed number and escape drops it, both handing the keys back to the knob
	function figureKeyed(e: KeyboardEvent) {
		if (e.key !== 'Enter' && e.key !== 'Escape') return;
		e.preventDefault();
		e.stopPropagation();
		dropped = e.key === 'Escape';
		knob?.focus({ preventScroll: true });
	}

	// What the value rests on in words, a named number by its name and a number with its unit
	const restWords = $derived.by(() => {
		if (restingText !== undefined) return restingText;
		if (resting === undefined || resting === '') return undefined;
		const n = Number(resting);
		if (!Number.isFinite(n)) return String(resting);
		return names.find((x) => x.value === n)?.label ?? [write(n), unit].filter(Boolean).join(' ');
	});
	// Named numbers offered as buttons, all but the one set and the one the value rests on
	const offers = $derived(names.filter((n) => n.value !== value && String(n.value) !== String(resting ?? '')));
	const spoken = $derived(value === undefined ? ['unset', restWords === undefined ? '' : `rests on ${restWords}`].filter(Boolean).join(', ') : [reading, name ? '' : unit].filter(Boolean).join(' '));
</script>

<div class="slider" class:ghost class:bare class:held class:pointed class:disabled class:dense={inline} style:--t={at} style:--stops={stops} style:--knob="{size}px">
	<span class="name"><FieldLabel {label} {info} forId={id} /></span>
	<label class="reading" class:empty={!typing && reading === ''}>
		<input
			bind:this={figure}
			class="figure"
			class:worded={!typing && name !== undefined}
			type="text"
			inputmode={decimals > 0 ? 'decimal' : 'numeric'}
			autocomplete="off"
			spellcheck="false"
			size="1"
			style:--chars={Math.max(5, (typing ? draft : reading).length)}
			value={typing ? draft : reading}
			aria-label={unit ? `${label} in ${unit}` : `${label} as a number`}
			{disabled}
			onfocus={() => !typing && edit()}
			oninput={typed}
			onblur={left}
			onkeydown={figureKeyed}
		/>{#if unit && (typing || !name)}<span class="unit">{unit}</span>{/if}
	</label>
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="rail" bind:this={rail} bind:clientWidth={width} onpointerdown={press} onpointermove={drag} onpointerup={release} onpointercancel={drop} onlostpointercapture={drop}>
		<span class="groove" class:stepped={stops > 0}>
			{#if !unset && !off}<span class="fill"></span>{/if}
		</span>
		<button
			bind:this={knob}
			{id}
			type="button"
			class="knob"
			role="slider"
			aria-label={label}
			aria-orientation="horizontal"
			aria-valuemin={span.min}
			aria-valuemax={span.max}
			aria-valuenow={value}
			aria-valuetext={spoken}
			{disabled}
			onkeydown={keyed}
			onblur={() => (pointed = false)}
		></button>
	</div>
	<div class="scale" aria-hidden="true">
		{#each scale as t, i (t.value)}
			<span class="mark" class:first={i === 0} class:last={i === scale.length - 1 && scale.length > 1} style:--at={t.at}>{t.label}</span>
		{/each}
	</div>
	{#if optional}
		<div class="resting"><Resting word={restingWord} text={restWords} offers={offers.map((n) => ({ label: n.label, pick: () => take(n.value) }))} set={value !== undefined} about={label} onreset={() => take(undefined)} /></div>
	{/if}
	{#if message}<span class="secondary message">{message}</span>{/if}
</div>

<style>
	/* The name and the box share a line over the rail, the scale under it */
	.slider {
		--knob: 20px;

		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		grid-template-areas:
			'name reading'
			'rail rail'
			'scale scale'
			'resting resting'
			'message message';
		column-gap: var(--nebu-space-3);
		align-items: center;
		min-width: 0;
	}
	.name {
		grid-area: name;
		min-width: 0;
	}
	.rail {
		position: relative;
		grid-area: rail;
		height: 28px;
		cursor: pointer;
		touch-action: pan-y;
	}
	.dense .rail {
		height: 22px;
	}
	.groove {
		position: absolute;
		top: 50%;
		right: 0;
		left: 0;
		height: 6px;
		margin-top: -3px;
		overflow: hidden;
		background: color-mix(in srgb, currentcolor 14%, var(--neo-background-color));
		border-radius: 999px;
		box-shadow: var(--neo-box-shadow-inset-1);
		container-type: inline-size;
	}
	/* A rail of few stops marks each one, dark on the bare rail and light under the fill */
	.stepped::before,
	.stepped .fill::after {
		--tile: calc((100cqw - var(--knob)) / var(--stops));

		position: absolute;
		inset: 0;
		background: radial-gradient(circle, var(--dot) 0 1px, transparent 1.5px) calc(var(--knob) / 2 - var(--tile) / 2) 50% / var(--tile) 100% repeat-x;
		content: '';
	}
	.stepped::before {
		--dot: color-mix(in srgb, currentcolor 45%, transparent);
	}
	.stepped .fill::after {
		--dot: color-mix(in srgb, var(--neo-background-color) 75%, transparent);
	}
	.fill {
		position: absolute;
		top: 0;
		bottom: 0;
		left: 0;
		width: calc(var(--knob) / 2 + var(--t) * (100% - var(--knob)));
		background: var(--neo-color-primary);
		border-radius: inherit;
		transition: width var(--nebu-normal) var(--nebu-settle);
	}
	.knob {
		position: absolute;
		top: 50%;
		left: calc(var(--t) * (100% - var(--knob)));
		display: grid;
		place-items: center;
		width: var(--knob);
		height: var(--knob);
		margin: calc(var(--knob) / -2) 0 0;
		padding: 0;
		background: light-dark(color-mix(in srgb, var(--neo-background-color), white 55%), color-mix(in srgb, var(--neo-background-color), white 30%));
		border: 0;
		border-radius: 50%;
		box-shadow:
			0 0 0 1px light-dark(rgb(0 0 0 / 6%), rgb(255 255 255 / 5%)),
			0 1px 2px light-dark(rgb(0 0 0 / 22%), rgb(0 0 0 / 60%)),
			0 3px 6px light-dark(rgb(0 0 0 / 12%), rgb(0 0 0 / 40%)),
			var(--nebu-machined);
		cursor: grab;
		transition:
			left var(--nebu-normal) var(--nebu-settle),
			scale var(--nebu-fast) var(--nebu-ease),
			opacity var(--nebu-fast) var(--nebu-ease);
		appearance: none;
		touch-action: none;
	}
	/* A set knob carries the accent on its face, one resting where an unset value falls is bare */
	.knob::after {
		width: 6px;
		height: 6px;
		background: var(--neo-color-primary);
		border-radius: 50%;
		content: '';
	}
	.ghost .knob::after {
		content: none;
	}
	/* A rail whose value rests on no number shows no knob until the keys reach it */
	.bare .knob {
		opacity: 0;
		pointer-events: none;
	}
	.bare .knob:focus-visible {
		opacity: 1;
	}
	.bare .knob::after {
		content: none;
	}
	.rail:hover .knob {
		scale: 1.08;
	}
	.held .knob {
		cursor: grabbing;
		scale: 0.96;
	}
	.held .knob,
	.held .fill {
		transition-property: scale;
	}
	.knob:focus-visible {
		outline: 2px solid var(--neo-color-primary);
		outline-offset: 2px;
	}
	.pointed .knob {
		outline: none;
	}
	.disabled .rail,
	.disabled .knob {
		cursor: not-allowed;
	}
	.disabled .fill,
	.disabled .knob::after {
		background: var(--nebu-text-faint);
	}
	.disabled .knob {
		box-shadow: 0 0 0 1px rgb(0 0 0 / 8%);
		scale: 1;
	}
	/* The box is a well the number is typed in, as wide as the number it holds */
	.reading {
		display: inline-flex;
		grid-area: reading;
		gap: 4px;
		align-items: baseline;
		min-width: 0;
		min-height: 26px;
		padding: 3px 8px;
		font-size: var(--nebu-text-secondary);
		white-space: nowrap;
		border-radius: 7px;
		box-shadow: var(--neo-box-shadow-inset-1);
		cursor: text;
		transition: box-shadow var(--nebu-fast) var(--nebu-ease);
	}
	.reading:focus-within {
		box-shadow: var(--neo-box-shadow-inset-2);
	}
	.figure {
		width: calc(var(--chars) * 1ch);
		min-width: 5ch;
		margin: 0;
		padding: 0;
		font: inherit;
		font-variant-numeric: tabular-nums;
		color: inherit;
		text-align: right;
		background: none;
		border: 0;
		outline: none;
	}
	@supports (field-sizing: content) {
		.figure {
			width: auto;
			field-sizing: content;
		}
	}
	.held .figure {
		color: var(--neo-color-primary);
	}
	.unit {
		color: var(--nebu-text-muted);
	}
	.disabled .reading {
		color: var(--nebu-disabled-text);
	}
	/* Each mark sits under its place on the rail, the ends inside the rail's own edges */
	.scale {
		position: relative;
		grid-area: scale;
		height: 16px;
		margin: 0 calc(var(--knob) / 2);
		font-size: 11px;
		font-variant-numeric: tabular-nums;
		line-height: 16px;
		color: var(--nebu-text-muted);
	}
	.mark {
		position: absolute;
		left: calc(var(--at) * 100%);
		white-space: nowrap;
		translate: -50% 0;
	}
	.mark::before {
		position: absolute;
		top: -5px;
		left: 50%;
		width: 1px;
		height: 4px;
		background: var(--nebu-border-strong);
		content: '';
	}
	.mark.first {
		left: calc(var(--knob) / -2);
		translate: 0 0;
	}
	.mark.last {
		right: calc(var(--knob) / -2);
		left: auto;
		translate: 0 0;
	}
	.mark.first::before,
	.mark.last::before {
		content: none;
	}
	.resting {
		grid-area: resting;
		min-width: 0;
	}
	.message {
		grid-area: message;
	}
</style>
