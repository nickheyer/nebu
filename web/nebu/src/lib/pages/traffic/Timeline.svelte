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
		const byLane = new Map<string, Trace[]>(endpoints.map((e) => [e, []]));
		for (const t of traces) {
			const key = laneOf(t, nodeName);
			if (!byLane.has(key)) byLane.set(key, []);
			byLane.get(key)!.push(t);
		}
		return [...byLane.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([lane, list]) => ({ lane, list, idle: list.every((t) => finished(t) !== undefined && now - (finished(t) ?? 0) > span / 4) }));
	});
	const height = $derived(lanes.reduce((h, l) => h + (l.idle ? 6 : laneHeight), 0) + 4);

	function x(t: number): number {
		return Math.min(100, Math.max(0, ((t - from) / span) * 100));
	}

	function facts(t: Trace) {
		const s = started(t);
		const f = firstToken(t);
		const e = finished(t);
		return [
			{ label: 'route', value: t.route },
			{ label: 'status', value: t.status ? String(t.status) : 'in flight' },
			{ label: 'tokens', value: `${num(t.promptTokens)} in, ${num(t.completionTokens)} out` },
			{ label: 'TTFT', value: f === undefined ? '' : ms(f - s) },
			{ label: 'took', value: e === undefined ? '' : ms(e - s) }
		].filter((x) => x.value !== '');
	}
</script>

<div class="timeline">
	<div class="lanes" style:height="{height}px">
		{#each lanes as lane (lane.lane)}
			<div class="lane" class:idle={lane.idle} style:height="{lane.idle ? 6 : laneHeight}px">
				<Tip text={lane.lane} tag="span"><span class="name mono ellipsis">{lane.idle ? '' : lane.lane}</span></Tip>
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
		{#if lanes.length === 0}
			<span class="secondary">No requests in the window</span>
		{/if}
	</div>
</div>

<style>
	.timeline {
		padding: 8px 0;
		border-bottom: 1px solid var(--nebu-border);
	}
	.lanes {
		display: flex;
		flex-direction: column;
		gap: 2px;
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
		flex: 0 0 16ch;
		max-width: 16ch;
		font-size: 12px;
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
