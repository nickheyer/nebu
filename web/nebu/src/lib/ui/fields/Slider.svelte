<script lang="ts">
	import { tick } from 'svelte';
	import { num } from '#lib/format.js';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import { decimalsOf, land, place, stride, type Curve, type Span } from '#lib/ui/fields/scale.js';
	import { fieldId } from '#lib/ui/fields/types.js';

	// A number dragged along a rail or typed over its reading, unset while its knob rests before the rail
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
		placeholder,
		optional = false,
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
		placeholder?: string;
		optional?: boolean;
		inline?: boolean;
		onchange?: (n: number | undefined) => void;
	} = $props();

	const id = fieldId();
	// Pixels between the rest and the rail
	const gap = 6;
	const span = $derived<Span>({ min, max, step, curve });
	const decimals = $derived(decimalsOf(step));
	const docked = $derived(optional && value === undefined);
	// The number an unset value comes to, when its placeholder is one
	const idle = $derived(placeholder !== undefined && placeholder.trim() !== '' && Number.isFinite(Number(placeholder)) ? Number(placeholder) : undefined);
	const at = $derived(value === undefined ? 0 : place(value, span));
	const ghost = $derived(docked && idle !== undefined ? place(idle, span) : 0);
	const reading = $derived(value === undefined ? '' : write(value));
	const hint = $derived(idle === undefined ? (placeholder ?? '') : write(idle));
	// The unit follows a number, never the words standing in for one
	const measure = $derived(value !== undefined || idle !== undefined ? unit : undefined);
	// Stops few enough to mark each one on the rail
	const stops = $derived(curve === 'linear' && max > min && (max - min) / step <= 10 ? Math.round((max - min) / step) : 0);

	let rail = $state<HTMLElement>();
	let knob = $state<HTMLButtonElement>();
	let figure = $state<HTMLInputElement>();
	let held = $state(false);
	// Whether the knob took the keys from a pointer, which draws no focus ring
	let pointed = $state(false);
	let typing = $state(false);
	let draft = $state('');
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

	// The value under a pointer, none while it is over the rest
	function under(e: PointerEvent): number | undefined {
		const box = rail!.getBoundingClientRect();
		const size = knob!.offsetWidth;
		const lead = optional ? size + gap : 0;
		const x = e.clientX - box.left - grab;
		if (optional && x < size + gap / 2) return undefined;
		return land((x - lead - size / 2) / (box.width - lead - size), span);
	}

	// Takes hold of the knob where it is, or brings it to a press anywhere else along the rail
	function press(e: PointerEvent) {
		if (disabled || e.button !== 0) return;
		e.preventDefault();
		const face = knob!.getBoundingClientRect();
		const off = e.clientX - (face.left + face.width / 2);
		const onKnob = Math.abs(off) <= face.width / 2 + 2;
		grab = onKnob ? off : 0;
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

	// Steps from the value, an unset one first taking the number it comes to or the rail's start
	function bump(up: boolean, times: number) {
		if (value === undefined) return take(idle ?? min);
		const next = value + (up ? step : -step) * times;
		take(Number(Math.min(Math.max(max, value), Math.max(Math.min(min, value), next)).toFixed(Math.max(decimals, decimalsOf(value)))));
	}

	// Opens the reading for typing, starting it with a character already pressed
	async function edit(first?: string) {
		typing = true;
		draft = first ?? (value === undefined ? '' : plain(value));
		await tick();
		figure?.focus({ preventScroll: true });
		if (first === undefined) figure?.select();
		else figure?.setSelectionRange(draft.length, draft.length);
	}

	// Arrows step, page keys stride, home and end take the rail's ends, delete unsets, a digit starts typing
	function keyed(e: KeyboardEvent) {
		pointed = false;
		if (disabled || e.metaKey || e.ctrlKey || e.altKey) return;
		const k = e.key;
		const numeric = (k >= '0' && k <= '9') || (k === '-' && min < 0) || ((k === '.' || k === ',') && decimals > 0);
		if (k === 'ArrowRight' || k === 'ArrowUp') bump(true, e.shiftKey ? 10 : 1);
		else if (k === 'ArrowLeft' || k === 'ArrowDown') bump(false, e.shiftKey ? 10 : 1);
		else if (k === 'PageUp' || k === 'PageDown') take(stride(value ?? idle ?? min, k === 'PageUp', span));
		else if (k === 'Home') take(min);
		else if (k === 'End') take(max);
		else if ((k === 'Delete' || k === 'Backspace') && optional) take(undefined);
		else if (k === 'Enter' || k === 'F2') void edit();
		else if (numeric) void edit(k === ',' ? '.' : k);
		else return;
		e.preventDefault();
	}

	// Keeps digits, one leading minus where the rail goes below zero and one point where steps are fractional
	function clean(s: string): string {
		let out = '';
		for (const ch of s) {
			if (ch >= '0' && ch <= '9') out += ch;
			else if (ch === '-' && min < 0 && out === '') out += ch;
			else if ((ch === '.' || ch === ',') && decimals > 0 && !out.includes('.')) out += '.';
		}
		return out;
	}

	function typed(e: Event & { currentTarget: HTMLInputElement }) {
		const text = clean(e.currentTarget.value);
		if (text !== e.currentTarget.value) e.currentTarget.value = text;
		draft = text;
	}

	// Takes the typed number as typed, past the rail's ends too, an emptied reading unsetting an optional value
	function commit() {
		if (draft === '') {
			if (optional) take(undefined);
		} else if (Number.isFinite(Number(draft))) take(Number(draft));
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
</script>

<div class="slider" class:optional class:docked class:held class:pointed class:disabled class:dense={inline} class:worded={!typing && value === undefined && idle === undefined} style:--t={at} style:--ghost={ghost} style:--stops={stops}>
	<span class="name"><FieldLabel {label} {info} forId={id} /></span>
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div class="rail" bind:this={rail} onpointerdown={press} onpointermove={drag} onpointerup={release} onpointercancel={drop} onlostpointercapture={drop}>
		{#if optional}<span class="rest"></span>{/if}
		<span class="groove" class:stepped={stops > 0}>
			{#if ghost > 0}<span class="planned"></span>{/if}
			{#if !docked}<span class="fill"></span>{/if}
		</span>
		<button
			bind:this={knob}
			{id}
			type="button"
			class="knob"
			role="slider"
			aria-label={label}
			aria-orientation="horizontal"
			aria-valuemin={min}
			aria-valuemax={max}
			aria-valuenow={value}
			aria-valuetext={[reading || hint, measure].filter(Boolean).join(' ') || undefined}
			{disabled}
			onkeydown={keyed}
			onblur={() => (pointed = false)}
		></button>
	</div>
	<span class="reading">
		<input
			bind:this={figure}
			class="figure"
			type="text"
			inputmode={decimals > 0 ? 'decimal' : 'numeric'}
			autocomplete="off"
			spellcheck="false"
			tabindex="-1"
			size="1"
			style:--chars={Math.max(1, (typing ? draft : reading || hint).length)}
			value={typing ? draft : reading}
			placeholder={hint}
			aria-label={`${label} typed`}
			{disabled}
			onfocus={() => !typing && edit()}
			oninput={typed}
			onblur={left}
			onkeydown={figureKeyed}
		/>{#if typing ? unit : measure}<span class="unit">{unit}</span>{/if}
	</span>
	{#if message}<span class="secondary message">{message}</span>{/if}
</div>

<style>
	/* The name and the reading share a line over the rail */
	.slider {
		--knob: 20px;
		--lead: 0px;

		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		grid-template-areas:
			'name reading'
			'rail rail'
			'message message';
		column-gap: var(--nebu-space-3);
		row-gap: 4px;
		align-items: center;
		min-width: 0;
	}
	.optional {
		--lead: calc(var(--knob) + 6px);
	}
	/* Words standing in for an unset number give way to the name, numbers never do */
	.worded {
		grid-template-columns: auto minmax(0, 1fr);
	}
	.dense {
		--knob: 16px;

		row-gap: 0;
	}
	.name {
		grid-area: name;
		min-width: 0;
	}
	.rail {
		position: relative;
		grid-area: rail;
		height: var(--nebu-input-height);
		cursor: pointer;
		touch-action: pan-y;
	}
	.dense .rail {
		height: 24px;
	}
	.groove,
	.rest {
		position: absolute;
		top: 50%;
		background: color-mix(in srgb, currentcolor 10%, var(--neo-background-color));
		border-radius: 999px;
		box-shadow: var(--neo-box-shadow-inset-1);
	}
	.groove {
		right: 0;
		left: var(--lead);
		height: 6px;
		margin-top: -3px;
		overflow: hidden;
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
	/* A notch of rail set apart, where the knob rests while the value is unset */
	.rest {
		left: 0;
		width: var(--knob);
		height: 6px;
		margin-top: -3px;
	}
	.fill,
	.planned {
		position: absolute;
		top: 0;
		bottom: 0;
		left: 0;
		border-radius: inherit;
	}
	.fill {
		width: calc(var(--knob) / 2 + var(--t) * (100% - var(--knob)));
		background: var(--neo-color-primary);
		transition: width var(--nebu-normal) var(--nebu-settle);
	}
	/* What an unset value comes to, hatched as planned memory is */
	.planned {
		width: calc(var(--knob) / 2 + var(--ghost) * (100% - var(--knob)));
		background: repeating-linear-gradient(135deg, var(--nebu-steel) 0 3px, transparent 3px 6px);
		transition: width var(--nebu-slow) var(--nebu-settle);
	}
	.knob {
		position: absolute;
		top: 50%;
		left: calc(var(--lead) + var(--t) * (100% - var(--lead) - var(--knob)));
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
			scale var(--nebu-fast) var(--nebu-ease);
		appearance: none;
		touch-action: none;
	}
	/* A set knob carries the accent on its face, a resting one is bare */
	.knob::after {
		width: 6px;
		height: 6px;
		background: var(--neo-color-primary);
		border-radius: 50%;
		content: '';
		transition: scale var(--nebu-normal) var(--nebu-settle);
	}
	.docked .knob {
		left: 0;
	}
	.docked .knob::after {
		scale: 0;
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
	.reading {
		display: inline-flex;
		grid-area: reading;
		align-items: baseline;
		justify-content: flex-end;
		min-width: 0;
		font-size: var(--nebu-text-secondary);
		white-space: nowrap;
	}
	/* The reading is plain text until the pointer or the keys reach it, then a shallow well to type in */
	.figure {
		width: calc(var(--chars) * 1ch + 10px);
		margin: -2px 0;
		padding: 2px 5px;
		font: inherit;
		font-variant-numeric: tabular-nums;
		color: inherit;
		text-align: right;
		background: none;
		border: 0;
		border-radius: 6px;
		outline: none;
		transition: box-shadow var(--nebu-fast) var(--nebu-ease);
	}
	@supports (field-sizing: content) {
		.figure {
			width: auto;
			min-width: calc(1ch + 10px);
			field-sizing: content;
		}
	}
	.worded .figure {
		flex: 0 1 auto;
		min-width: 0;
		text-overflow: ellipsis;
	}
	.figure:hover:not(:disabled),
	.figure:focus {
		box-shadow: var(--neo-box-shadow-inset-1);
	}
	.held .figure {
		color: var(--neo-color-primary);
	}
	.figure::placeholder {
		color: var(--nebu-text-faint);
		opacity: 1;
	}
	/* Digits with no unit after them end on the rail's end, their well reaching past it */
	.figure:last-child {
		margin-right: -5px;
	}
	.unit {
		color: var(--nebu-text-muted);
	}
	.disabled .reading {
		color: var(--nebu-disabled-text);
	}
	.message {
		grid-area: message;
	}
</style>
