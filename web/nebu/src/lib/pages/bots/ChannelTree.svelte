<script lang="ts">
	import type { BotGuild } from '#lib/proto/nebu/v1/bot_pb.js';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';

	let { value = $bindable([]), guilds, label, info }: { value?: string[]; guilds: BotGuild[]; label: string; info?: string } = $props();

	function ofGuild(g: BotGuild): string[] {
		const ids = new Set(g.channels.map((c) => c.id));
		return value.filter((id) => ids.has(id));
	}

	function setGuild(g: BotGuild, picked: string[]) {
		const ids = new Set(g.channels.map((c) => c.id));
		value = [...value.filter((id) => !ids.has(id)), ...picked];
	}
</script>

<Stack gap={4}>
	<FieldLabel {label} {info} />
	{#if guilds.length === 0}
		<span class="secondary">Not connected</span>
	{:else}
		{#each guilds as g (g.id)}
			<div class="guild">
				<Checklist bind:value={() => ofGuild(g), (v) => setGuild(g, v)} options={g.channels.map((c) => ({ value: c.id, label: `#${c.name}`, description: c.kind }))} label={g.name} empty="No channels" />
			</div>
		{/each}
	{/if}
</Stack>

<style>
	.guild {
		display: flex;
		flex-direction: column;
		min-width: 0;
		padding-left: 12px;
	}
</style>
