<script lang="ts">
	import { reason } from '#lib/api/transport.js';
	import { live } from '#lib/live/events.svelte.js';
	import { DiskLimitsSchema, DownloadsSchema } from '#lib/proto/nebu/v1/settings_pb.js';
	import type { HeaderTab } from '#lib/shell/chrome.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Bytes from '#lib/ui/fields/Bytes.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import Columns from '#lib/ui/layout/Columns.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import BrandPlate from '#lib/pages/settings/BrandPlate.svelte';
	import DownloadWindows from '#lib/pages/settings/DownloadWindows.svelte';
	import { SectionDraft, SettingsForm } from '#lib/pages/settings/form.svelte.js';

	let { tabs }: { tabs: HeaderTab[] } = $props();

	const form = new SettingsForm();
	const downloads = new SectionDraft(form, DownloadsSchema, (s) => s.downloads, (s, v) => (s.downloads = v), 'Downloads saved');
	const storage = new SectionDraft(form, DiskLimitsSchema, (s) => s.storage, (s, v) => (s.storage = v), 'Storage saved');

	const levels = ['debug', 'info', 'warn', 'error'].map((l) => ({ value: l, label: l[0]!.toUpperCase() + l.slice(1) }));
	const storedLabel = $derived(form.stored?.hostLabel ?? '');
	const storedLevel = $derived(form.stored?.logLevel || form.defaults?.logLevel || '');
	let hostLabel = $state('');
	let logLevel = $state('');
	let hostSaving = $state(false);
	let hostError = $state<string | undefined>(undefined);
	const hostDirty = $derived(hostLabel.trim() !== storedLabel || logLevel !== storedLevel);

	$effect(() => {
		hostLabel = storedLabel;
		logLevel = storedLevel;
	});

	async function saveHost() {
		hostSaving = true;
		hostError = undefined;
		try {
			await form.save((s) => {
				s.hostLabel = hostLabel.trim();
				s.logLevel = logLevel;
			}, 'Host saved');
		} catch (err) {
			hostError = reason(err);
		} finally {
			hostSaving = false;
		}
	}

	function discardHost() {
		hostLabel = storedLabel;
		logLevel = storedLevel;
		hostError = undefined;
	}
</script>

<PageHeader {tabs} active="general" />

{#if form.error && !form.ready}
	<Card><Empty text="Settings failed to load" hint={form.error} action="Retry" onclick={() => form.load()} /></Card>
{:else if !form.ready}
	<Card><Skeleton rows={4} kind="facts" /></Card>
{:else}
	<Columns ratio="1/1">
		{#snippet left()}
			<BrandPlate />
			<Card title="Host">
				<FieldRow>
					<Text bind:value={hostLabel} label="Host label" placeholder={live.host?.hostname} max={64} />
					<Select bind:value={logLevel} options={levels} label="Log level" />
				</FieldRow>
				<SaveBar dirty={hostDirty} saving={hostSaving} error={hostError} onsave={saveHost} ondiscard={discardHost} />
			</Card>
		{/snippet}
		{#snippet right()}
			<Card title="Downloads">
				<FieldRow>
					<NumberField bind:value={downloads.draft.connections} min={1} max={64} label="Connections per file" />
					<NumberField bind:value={downloads.draft.retries} min={1} max={100} label="Tries per chunk" />
				</FieldRow>
				<Bytes bind:value={() => Number(downloads.draft.maxBytesPerSecond), (n) => (downloads.draft.maxBytesPerSecond = BigInt(n))} label="Speed limit per second" emptyZero />
				<DownloadWindows bind:value={downloads.draft.windows} />
				<SaveBar dirty={downloads.dirty} saving={downloads.saving} error={downloads.error} onsave={() => downloads.save()} ondiscard={() => downloads.discard()} />
			</Card>
			<Card title="Storage">
				<FieldRow wide>
					<Bytes bind:value={() => Number(storage.draft.maxBytes), (n) => (storage.draft.maxBytes = BigInt(n))} label="Store limit" info="Unused models leave first once the store is over it" emptyZero />
					<Bytes bind:value={() => Number(storage.draft.minFreeBytes), (n) => (storage.draft.minFreeBytes = BigInt(n))} label="Keep free" info="Disk a download leaves free" emptyZero />
				</FieldRow>
				<SaveBar dirty={storage.dirty} saving={storage.saving} error={storage.error} onsave={() => storage.save()} ondiscard={() => storage.discard()} />
			</Card>
		{/snippet}
	</Columns>
{/if}
