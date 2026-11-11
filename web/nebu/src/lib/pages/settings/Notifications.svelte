<script lang="ts">
	import { clone, create } from '@bufbuild/protobuf';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { NotificationsSchema, WebhookSchema, type Webhook } from '#lib/proto/nebu/v1/settings_pb.js';
	import type { HeaderTab } from '#lib/shell/chrome.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import Sheet from '#lib/ui/layout/Sheet.svelte';
	import WebhookEditor from '#lib/pages/settings/WebhookEditor.svelte';
	import { SettingsForm } from '#lib/pages/settings/form.svelte.js';
	import { webhookEvents, webhookFormats } from '#lib/pages/settings/webhooks.js';
	import BellIcon from 'phosphor-svelte/lib/BellIcon';

	let { tabs }: { tabs: HeaderTab[] } = $props();

	const form = new SettingsForm();
	// The stored webhooks, or else the ones the config file names
	const webhooks = $derived((form.stored?.notifications ?? form.defaults?.notifications)?.webhooks ?? []);
	let open = $state(false);
	let editing = $state<Webhook | undefined>(undefined);
	let toggleFailures = $state(0);

	function edit(w: Webhook | undefined) {
		editing = w;
		open = true;
	}

	function close() {
		open = false;
		editing = undefined;
	}

	// Stores the whole list with one change made to it
	async function write(change: (list: Webhook[]) => Webhook[], saved: string) {
		const next = change(webhooks.map((w) => clone(WebhookSchema, w)));
		await form.save((s) => (s.notifications = create(NotificationsSchema, { webhooks: next })), saved);
	}

	// Replaces the webhook being edited, or adds a new one under a name no other holds
	async function put(w: Webhook) {
		w.name = w.name.trim();
		if (webhooks.some((x) => x.name === w.name && x.id !== w.id)) throw new Error(`a webhook named ${w.name} exists`);
		await write((list) => (w.id ? list.map((x) => (x.id === w.id ? w : x)) : [...list, w]), `Saved ${w.name}`);
	}

	async function toggle(w: Webhook, on: boolean) {
		try {
			await write((list) => list.map((x) => (x.id === w.id ? { ...x, enabled: on } : x)), `${w.name} ${on ? 'enabled' : 'disabled'}`);
		} catch (err) {
			toggleFailures++;
			toast.error(`${w.name} failed to ${on ? 'enable' : 'disable'}`, reason(err));
		}
	}

	async function remove(w: Webhook) {
		try {
			await write((list) => list.filter((x) => x.id !== w.id), `Deleted ${w.name}`);
			if (editing?.id === w.id) close();
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}

	async function test(w: Webhook) {
		try {
			const res = await rpc.settings.testWebhook({ webhook: w });
			if (res.status >= 200 && res.status < 300) toast.success(`${w.name} answered ${res.status}`);
			else toast.error(`${w.name} answered ${res.status}`);
		} catch (err) {
			toast.error('Test failed', reason(err));
		}
	}

	function hostOf(url: string): string {
		return url.replace(/^[a-z][a-z0-9+.-]*:\/\//i, '').split('/')[0] ?? url;
	}

	function eventsOf(w: Webhook): string {
		return webhookEvents.filter((e) => w.events.includes(e.value)).map((e) => e.label).join(', ');
	}
</script>

{#snippet list()}
	<Card title="Webhooks" flush>
		{#snippet tools()}
			<NeoButton class="primary" elevation={2} onclick={() => edit(undefined)}>Add webhook</NeoButton>
		{/snippet}
		{#if form.error && !form.ready}
			<Empty text="Settings failed to load" hint={form.error} action="Retry" onclick={() => form.load()} />
		{:else if !form.ready}
			<Skeleton rows={3} secondary="16ch" facts={2} />
		{:else if webhooks.length === 0}
			<Empty icon={BellIcon} text="No webhooks" action={open ? undefined : 'Add webhook'} onclick={() => edit(undefined)} />
		{:else}
			<List head={{ primary: 'Webhook', secondary: 'Host', facts: ['Format', 'Events'], action: 'Enabled' }}>
				{#each webhooks as w (w.id)}
					<ListRow
						lamp={w.enabled ? 'success' : 'muted'}
						shape={w.enabled ? 'filled' : 'hollow'}
						primary={w.name}
						secondary={hostOf(w.url)}
						secondaryKind="address"
						secondaryTip={w.url}
						secondaryWidth="24ch"
						facts={[{ value: webhookFormats.find((f) => f.value === w.format)?.label ?? '' }, { value: eventsOf(w) }]}
						selected={open && editing?.id === w.id}
						onclick={() => edit(w)}
						menu={[
							{ label: 'Test', onclick: () => test(w) },
							{ label: 'Delete', danger: true, divider: true, confirm: `Delete ${w.name}?`, onclick: () => remove(w) }
						]}
					>
						{#snippet action()}
							{#key `${w.enabled}:${toggleFailures}`}<Switch checked={w.enabled} onchange={(on) => toggle(w, on)} />{/key}
						{/snippet}
					</ListRow>
				{/each}
			</List>
		{/if}
	</Card>
{/snippet}

<PageHeader {tabs} active="notifications" />

<Sheet {open} onclose={close} {list}>
	{#if open}
		{#key editing?.id ?? 'new'}
			<WebhookEditor webhook={editing} onsave={put} onclose={close} />
		{/key}
	{/if}
</Sheet>
