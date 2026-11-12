<script lang="ts">
	import { plural } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { ConditionStatus } from '#lib/proto/nebu/v1/meta_pb.js';
	import type { Install, RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import { installLabel } from '#lib/pages/serve/serve.js';

	// A runtime and one of its installs, an empty install standing for the newest
	let {
		// Unset until the daemon has listed them
		runtimes,
		runtimeId = $bindable(''),
		installId = $bindable(''),
		// Lets the runtime stay empty, a launch then picking one for its model
		optional = false
	}: { runtimes: RuntimeStatus[] | undefined; runtimeId?: string; installId?: string; optional?: boolean } = $props();

	const newestFirst = (a: Install, b: Install) => Number(b.createdAt?.seconds ?? 0n) - Number(a.createdAt?.seconds ?? 0n);
	const installs = $derived([...live.installs.values()].filter((i) => i.runtimeId === runtimeId).sort(newestFirst));
	const installed = $derived(new Set([...live.installs.values()].map((i) => i.runtimeId)));
	// Every runtime with an install, and the chosen one whatever it has
	const runtimeOptions = $derived(
		(runtimes ?? [])
			.flatMap((r) => (r.runtime ? [r.runtime] : []))
			.filter((rt) => installed.has(rt.id) || rt.id === runtimeId)
			.map((rt) => ({ value: rt.id, label: rt.name || rt.id }))
	);
	const installOptions = $derived(installs.map((i) => ({ value: i.id, label: name(i), description: unmet(i) || made(i) })));
	// The install a launch runs, the pinned one or the newest
	const running = $derived(installs.find((i) => i.id === installId) ?? installs[0]);
	const known = $derived(runtimes?.some((r) => r.runtime?.id === runtimeId) === true);

	// An install by its version and the variant it was built as
	function name(i: Install): string {
		return [installLabel(i), live.builds.get(i.buildId)?.variant].filter(Boolean).join(' ');
	}

	// A build's ref and patch count, else the release or binary the install came from
	function made(i: Install): string {
		const b = live.builds.get(i.buildId);
		if (!b) return i.origin || i.path;
		return [b.ref, b.patches.length ? plural(b.patches.length, 'patch', 'patches') : ''].filter(Boolean).join(', ');
	}

	function unmet(i: Install): string {
		return (i.status?.conditions ?? [])
			.filter((c) => c.status === ConditionStatus.FALSE)
			.map((c) => c.message || c.reason)
			.join(', ');
	}

	// Another runtime drops the pinned install, which belonged to the one before
	function setRuntime(id: string) {
		if (!runtimes || id === runtimeId) return;
		runtimeId = id;
		installId = '';
	}
</script>

<FieldRow>
	<Select bind:value={() => (runtimes ? runtimeId || undefined : undefined), (v) => setRuntime(v ?? '')} options={runtimeOptions} label="Runtime" loading={!runtimes} placeholder={optional && runtimes ? 'Any' : undefined} clearable={optional} error={known && runtimeId !== '' && installs.length === 0 ? 'Not installed' : undefined} />
	{#if running}
		<Select bind:value={() => installId || undefined, (v) => (installId = v ?? '')} options={installOptions} label="Install" info="Left empty, each launch runs the newest" placeholder={name(installs[0]!)} clearable error={unmet(running) || undefined} />
	{/if}
</FieldRow>
