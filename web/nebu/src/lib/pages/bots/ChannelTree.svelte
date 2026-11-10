<script lang="ts">
	import type { BotGuild } from '#lib/proto/nebu/v1/bot_pb.js';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import FieldLabel from '#lib/ui/fields/FieldLabel.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';

	// The channels of every server the bot is in, one checkbox each under its server
	let { value = $bindable([]), guilds, label, info }: { value?: string[]; guilds: BotGuild[]; label?: string; info?: string } = $props();

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
	{#each guilds as g (g.id)}
		<Checklist bind:value={() => ofGuild(g), (v) => setGuild(g, v)} options={g.channels.map((c) => ({ value: c.id, label: `#${c.name}`, description: c.kind }))} label={g.name} />
	{/each}
</Stack>
