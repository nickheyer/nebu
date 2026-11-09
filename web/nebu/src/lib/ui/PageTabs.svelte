<script lang="ts">
	import { goto } from '$app/navigation';
	import { NeoTab, NeoTabs } from '#lib/neo/nav/index.js';
	import { viewport } from '#lib/state/viewport.svelte.js';
	import Select from '#lib/ui/fields/Select.svelte';

	export type Tab = { id: string; label: string; href?: string };

	// A strip of tabs, a select on a phone past four
	let {
		tabs,
		active,
		base,
		onchange
	}: { tabs: Tab[]; active: string; base?: string; onchange?: (id: string) => void } = $props();

	function change(id?: string) {
		if (!id || id === active) return;
		if (onchange) onchange(id);
		else if (base !== undefined) void goto(`${base}/${id}`);
	}
</script>

{#if viewport.mobile && tabs.length > 4}
	<Select value={active} options={tabs.map((t) => ({ value: t.id, label: t.label }))} onchange={(v) => change(v)} />
{:else}
	<NeoTabs {active} onchange={(id) => change(id as string)} line slide elevation={0} borderless tag="nav" aria-label="Tabs" class="nebu-tabs">
		{#each tabs as t (t.id)}
			<NeoTab tabId={t.id}>
				<span class="tab">{t.label}</span>
			</NeoTab>
		{/each}
	</NeoTabs>
{/if}

<style>
	.tab {
		display: inline-flex;
		align-items: baseline;
	}
</style>
