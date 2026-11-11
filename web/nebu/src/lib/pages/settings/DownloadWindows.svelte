<script lang="ts">
	import { create } from '@bufbuild/protobuf';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { TransferWindowSchema, type TransferWindow } from '#lib/proto/nebu/v1/config_pb.js';
	import Bytes from '#lib/ui/fields/Bytes.svelte';
	import MultiSelect from '#lib/ui/fields/MultiSelect.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import XIcon from 'phosphor-svelte/lib/XIcon';

	// The times of the week downloads run at another speed or wait, the first one holding the current time winning
	let { value = $bindable([]) }: { value?: TransferWindow[] } = $props();

	const week = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];
	const days = week.map((d) => ({ value: d, label: d[0]!.toUpperCase() + d.slice(1) }));

	// The days a window names, a range such as fri-mon running through the weekend
	function daysOf(spec: string): string[] {
		const out = new Set<string>();
		for (const part of spec.toLowerCase().split(',')) {
			const [from, to] = part.split('-').map((d) => week.indexOf(d.trim().slice(0, 3)));
			if (from === undefined || from < 0) continue;
			const end = to === undefined || to < 0 ? from : to;
			for (let i = from; ; i = (i + 1) % 7) {
				out.add(week[i]!);
				if (i === end) break;
			}
		}
		return week.filter((d) => out.has(d));
	}
</script>

{#each value as w, i (i)}
	<Row gap={8} align="flex-end">
		<Cell min="9rem"><MultiSelect bind:value={() => daysOf(w.days), (v) => (w.days = v.join(','))} options={days} label={i === 0 ? 'Days' : undefined} /></Cell>
		<Cell min="6.5rem" grow={false}><Text bind:value={w.from} type="time" label={i === 0 ? 'From' : undefined} /></Cell>
		<Cell min="6.5rem" grow={false}><Text bind:value={w.to} type="time" label={i === 0 ? 'To' : undefined} /></Cell>
		<Cell min="13rem"><Bytes bind:value={() => Number(w.maxBytesPerSecond), (n) => (w.maxBytesPerSecond = BigInt(n))} label={i === 0 ? 'Speed limit per second' : undefined} disabled={w.pause} emptyZero /></Cell>
		<Cell grow={false}><Switch bind:checked={w.pause} label="Pause" compact /></Cell>
		<Cell grow={false}>
			<NeoButton text aria-label="Remove window" onclick={() => (value = value.filter((_, j) => j !== i))}>
				{#snippet icon()}<XIcon size={20} />{/snippet}
			</NeoButton>
		</Cell>
	</Row>
{/each}
<Row>
	<NeoButton text onclick={() => (value = [...value, create(TransferWindowSchema)])}>
		{#snippet icon()}<PlusIcon size={16} />{/snippet}
		Window
	</NeoButton>
</Row>
