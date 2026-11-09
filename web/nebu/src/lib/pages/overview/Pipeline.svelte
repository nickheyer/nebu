<script lang="ts" module>
	import type { Lamp } from '#lib/ui/status.js';

	export type Stage = {
		id: string;
		label: string;
		value: string;
		unit: string;
		detail: string;
		href: string;
		lamp?: Lamp;
		done: boolean;
		action?: { label: string; href: string };
		spark?: number[];
	};
</script>

<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import Dot from '#lib/ui/Dot.svelte';
	import Sparkline from '#lib/ui/Sparkline.svelte';
	import CaretRightIcon from 'phosphor-svelte/lib/CaretRightIcon';

	// Lights each stage that holds something and offers the first empty stage its next step
	let { stages }: { stages: Stage[] } = $props();

	const firstEmpty = $derived(stages.findIndex((s) => !s.done));
</script>

<div class="pipewrap">
	<ol class="pipe">
		{#each stages as s, i (s.id)}
			{#if i > 0}
				<li class="link" class:lit={stages[i - 1]?.done} aria-hidden="true">
					<span class="wire"></span>
					<CaretRightIcon size={14} weight="bold" />
				</li>
			{/if}
			<li class="stage" class:empty={!s.done} class:next={i === firstEmpty}>
				<a class="body" href={s.href}>
					<span class="label">{#if s.lamp}<Dot lamp={s.lamp} size={8} />{/if}{s.label}</span>
					<span class="value">{s.value}<span class="unit">{s.unit}</span></span>
					{#if s.detail}<span class="detail">{s.detail}</span>{/if}
				</a>
				{#if s.spark?.some((v) => v > 0)}
					<span class="spark"><Sparkline points={s.spark} width={96} height={22} /></span>
				{:else if s.action && i === firstEmpty}
					<span class="cta"><NeoButton class="primary" elevation={2} href={s.action.href}>{s.action.label}</NeoButton></span>
				{:else if s.action}
					<a class="later" href={s.action.href}>{s.action.label}</a>
				{/if}
			</li>
		{/each}
	</ol>
</div>

<style>
	.pipewrap {
		container-type: inline-size;
	}
	.pipe {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: var(--nebu-space-4);
		margin: 0;
		padding: 0;
		list-style: none;
	}
	@container (min-width: 520px) {
		.pipe {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}
	}
	@container (min-width: 860px) {
		.pipe {
			grid-template-columns: repeat(4, minmax(0, max-content) minmax(40px, 1fr)) minmax(0, max-content);
			gap: 0;
		}
	}
	.link {
		display: none;
	}
	@container (min-width: 860px) {
		.link {
			display: flex;
			align-items: center;
			align-self: start;
			justify-content: center;
			height: 72px;
			color: var(--nebu-border-strong);
		}
	}
	.wire {
		flex: 0 0 26px;
		height: 2px;
		background: repeating-linear-gradient(90deg, var(--nebu-border-strong) 0 4px, transparent 4px 8px);
		border-radius: 2px;
	}
	.link :global(svg) {
		flex: 0 0 auto;
		margin-left: -4px;
	}
	.link.lit {
		color: var(--neo-color-primary);
	}
	.link.lit .wire {
		background: var(--neo-color-primary);
	}
	.stage {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-3);
		min-width: 0;
		padding: var(--nebu-space-2) var(--nebu-space-3);
	}
	.body {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
		color: inherit;
		text-decoration: none;
		border-radius: var(--neo-border-radius);
	}
	.body:hover .value {
		color: var(--neo-color-primary);
	}
	.label {
		display: inline-flex;
		gap: 6px;
		align-items: center;
		font-size: var(--nebu-text-secondary);
		font-weight: 400;
		color: var(--nebu-text-muted);
	}
	.value {
		overflow: hidden;
		font-size: 24px;
		font-weight: 300;
		font-variant-numeric: tabular-nums;
		line-height: 1.25;
		animation: nebu-reveal var(--nebu-slow) var(--nebu-settle) backwards;
		white-space: nowrap;
		text-overflow: ellipsis;
		transition: color var(--nebu-fast) var(--nebu-ease);
	}
	.unit {
		margin-left: 6px;
		font-size: var(--nebu-text-secondary);
		font-weight: 400;
		color: var(--nebu-text-muted);
	}
	.empty .value,
	.empty .unit {
		color: var(--nebu-text-faint);
	}
	.next .value {
		color: var(--neo-text-color);
	}
	.detail {
		overflow: hidden;
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		white-space: nowrap;
		text-overflow: ellipsis;
	}
	.spark {
		display: inline-flex;
		color: var(--neo-color-primary);
	}
	.cta {
		display: inline-flex;
	}
	@container (max-width: 519px) {
		.pipe {
			gap: var(--nebu-space-3);
		}
		.stage {
			gap: var(--nebu-space-2);
			padding: 0;
		}
		.value {
			font-size: 18px;
		}
	}
	.later {
		font-size: var(--nebu-text-secondary);
		color: var(--nebu-text-muted);
		text-decoration: none;
	}
	.later:hover {
		color: var(--neo-color-primary);
	}
</style>
