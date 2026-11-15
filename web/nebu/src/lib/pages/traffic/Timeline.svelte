<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import type { Trace } from '#lib/proto/nebu/v1/gateway_pb.js';
	import { ms, num } from '#lib/format.js';
	import Tip from '#lib/ui/Tip.svelte';
	import { failed, finished, firstToken, laneOf, started } from '#lib/pages/traffic/traffic.js';

	let {
		traces,
		endpoints,
		from,
		to,
		now,
		href,
		nodeName
	}: { traces: Trace[]; endpoints: string[]; from: number; to: number; now: number; href: (id: string) => string; nodeName: (id: string) => string } = $props();

	const laneHeight = 18;
	const span = $derived(Math.max(1, to - from));
	// One lane per known endpoint and per lane a trace ran in, idle once nothing ran in the window's last quarter
	const lanes = $derived.by(() => {
		const byLane = new Map<string, { address: boolean; list: Trace[] }>(endpoints.map((e) => [e, { address: true, list: [] }]));
		for (const t of traces) {
			const { name, address } = laneOf(t, nodeName);
			if (!byLane.has(name)) byLane.set(name, { address, list: [] });
			byLane.get(name)!.list.push(t);
		}
		return [...byLane.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([lane, { address, list }]) => ({ lane, address, list, idle: list.every((t) => finished(t) !== undefined && now - (finished(t) ?? 0) > span / 4) }));
	});
	const height = $derived(Math.max(laneHeight * 2, lanes.reduce((h, l) => h + (l.idle ? 6 : laneHeight), 0) + 4));
	// Spans between marks a scale may use, in milliseconds, from a second to a day
	const steps = [1, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 21_600, 43_200, 86_400].map((s) => s * 1000);
	let width = $state(0);
	const live = $derived(now - to < 2000);
	// Marks counted back from the window's end at the smallest whole span that keeps their labels apart
	const marks = $derived.by(() => {
		const room = Math.max(2, Math.floor(width / (live ? 84 : 72)));
		const step = steps.find((s) => span / s <= room - 1) ?? steps[steps.length - 1]!;
		const out: { at: number; label: string }[] = [];
		for (let back = 0; back <= span; back += step) out.push({ at: 100 - (back / span) * 100, label: back === 0 && live ? 'now' : live ? `${since(back)} ago` : clock(to - back, step) });
		return out.reverse();
	});

	// A span in its largest whole unit
	function since(span: number): string {
		const s = Math.round(span / 1000);
		return s % 3600 === 0 ? `${s / 3600} h` : s % 60 === 0 ? `${s / 60} min` : `${s} s`;
	}

	// A moment as the clock reads it, with seconds only on a scale that steps by them
	function clock(t: number, step: number): string {
		return new Date(t).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: step < 60_000 ? '2-digit' : undefined });
	}

	function x(t: number): number {
		return Math.min(100, Math.max(0, ((t - from) / span) * 100));
	}

	function facts(t: Trace) {
		const s = started(t);
		const f = firstToken(t);
		const e = finished(t);
		return [
			{ label: 'route', value: t.route },
			{ label: 'status', value: t.status ? String(t.status) : 'running' },
			{ label: 'tokens', value: `${num(t.promptTokens)} in, ${num(t.completionTokens)} out` },
			{ label: 'Time to first token', value: f === undefined ? '' : ms(f - s) },
			{ label: 'took', value: e === undefined ? '' : ms(e - s) }
		].filter((x) => x.value !== '');
	}
</script>

<!-- The plot stands with its scale and its key whether or not a request ran in the window -->
<div class="timeline" role="group" aria-label="Requests over time">
	<div class="plot">
		<div class="grid" aria-hidden="true">
			{#each marks as m (m.at)}<span class="rule" style:left="{m.at}%"></span>{/each}
		</div>
		{#if traces.length === 0}<span class="none secondary">No request ran in this window</span>{/if}
	<div class="lanes" style:height="{height}px">
		{#each lanes as lane (lane.lane)}
			<div class="lane" class:idle={lane.idle} style:height="{lane.idle ? 6 : laneHeight}px">
				<Tip text={lane.lane} tag="span"><span class="name ellipsis" class:mono={lane.address}>{lane.idle ? '' : lane.lane}</span></Tip>
				<div class="bars">
					{#each lane.list as t (t.id)}
						{@const s = started(t)}
						{@const f = firstToken(t) ?? finished(t) ?? now}
						{@const e = finished(t) ?? now}
						<Tip facts={facts(t)} tag="span" class="bar-wrap">
							<NeoButton container text href={href(t.id)} class="bar {failed(t) ? 'bad' : ''}" style="left: {x(s)}%; width: {Math.max(0.3, x(e) - x(s))}%" aria-label="Open trace {t.id}">
								<span class="wait" style:width="{Math.min(100, ((f - s) / Math.max(1, e - s)) * 100)}%"></span>
							</NeoButton>
						</Tip>
					{/each}
				</div>
			</div>
		{/each}
	</div>
	</div>
	<div class="axis" aria-hidden="true" bind:clientWidth={width}>
		{#each marks as m (m.at)}<span class="tick" class:first={m.at < 4} class:last={m.at > 96} style:left="{m.at}%">{m.label}</span>{/each}
	</div>
	<div class="key">
		<span class="item"><span class="swatch wait-key"></span>Waiting for the first token</span>
		<span class="item"><span class="swatch run-key"></span>Writing the answer</span>
		<span class="item"><span class="swatch bad-key"></span>Failed</span>
	</div>
</div>

<style>
	/* Lanes run edge to edge of the list card at its padding, the list head under them drawing the rule */
	.timeline {
		--name: 9rem;

		padding: var(--nebu-space-1) var(--nebu-card-pad, 0) var(--nebu-space-3);
		container-type: inline-size;
	}
	@container (max-width: 30rem) {
		.plot,
		.axis {
			--name: 5.5rem;
		}
	}
	/* The plot leaves the width of a lane's name before its bars, so the scale under it lines up with them */
	.plot {
		position: relative;
	}
	.grid,
	.axis {
		margin-left: calc(var(--name) + 8px);
	}
	.grid {
		position: absolute;
		inset: 0;
	}
	.rule {
		position: absolute;
		top: 0;
		bottom: 0;
		width: 1px;
		background: var(--nebu-border);
	}
	.none {
		position: absolute;
		top: 50%;
		left: calc(50% + (var(--name) + 8px) / 2);
		z-index: 1;
		padding: 0 var(--nebu-space-2);
		white-space: nowrap;
		background: var(--neo-background-color);
		translate: -50% -50%;
	}
	.lanes {
		position: relative;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}
	.axis {
		position: relative;
		height: 18px;
		margin-top: 4px;
		font-size: 11px;
		font-variant-numeric: tabular-nums;
		line-height: 18px;
		color: var(--nebu-text-muted);
		border-top: 1px solid var(--nebu-border-strong);
	}
	.tick {
		position: absolute;
		white-space: nowrap;
		translate: -50% 0;
	}
	.tick.first {
		translate: 0 0;
	}
	.tick.last {
		translate: -100% 0;
	}
	.key {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-1) var(--nebu-space-4);
		margin-top: var(--nebu-space-2);
		font-size: var(--nebu-text-caption);
		color: var(--nebu-text-muted);
	}
	.item {
		display: inline-flex;
		gap: 6px;
		align-items: center;
	}
	.swatch {
		width: 14px;
		height: 8px;
		border-radius: 2px;
	}
	.wait-key {
		background: var(--neo-color-warning);
	}
	.run-key {
		background: var(--neo-color-success);
	}
	.bad-key {
		background: var(--neo-color-error);
	}
	.lane {
		display: flex;
		gap: 8px;
		align-items: center;
	}
	.lane.idle .bars {
		border-top: 1px solid var(--nebu-border);
	}
	.name {
		display: inline-block;
		flex: 0 0 var(--name);
		max-width: var(--name);
		font-size: var(--nebu-text-caption);
		color: var(--nebu-text-muted);
	}
	.bars {
		position: relative;
		flex: 1 1 auto;
		height: 100%;
	}
	:global(.bar-wrap) {
		position: static;
	}
	.bars :global(.neo-button.bar) {
		position: absolute;
		top: 2px;
		bottom: 2px;
		min-height: 0;
		overflow: hidden;
		background: var(--neo-color-success);
		border-radius: 3px;
	}
	.bars :global(.neo-button.bar.bad) {
		background: var(--neo-color-error);
	}
	.wait {
		position: absolute;
		top: 0;
		bottom: 0;
		left: 0;
		display: block;
		background: var(--neo-color-warning);
	}
</style>
