<script lang="ts">
	import { clone, create, equals } from '@bufbuild/protobuf';
	import { untrack } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { NotifyEvent, WebhookFormat, WebhookSchema, type Webhook } from '#lib/proto/nebu/v1/settings_pb.js';
	import Dot from '#lib/ui/Dot.svelte';
	import Checklist from '#lib/ui/fields/Checklist.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { webhookEvents, webhookFormats } from '#lib/pages/settings/webhooks.js';

	// One webhook being added or changed, written back through the list that holds it
	let { webhook, onsave, onclose }: { webhook?: Webhook; onsave: (w: Webhook) => Promise<void>; onclose: () => void } = $props();

	const draft = untrack(() => webhook);
	const editing = draft !== undefined;
	let w = $state(draft ? clone(WebhookSchema, draft) : create(WebhookSchema, { enabled: true, format: WebhookFormat.EVENT, events: [NotifyEvent.MODEL_FAILED, NotifyEvent.TASK_FAILED] }));
	const baseline = untrack(() => clone(WebhookSchema, w));
	const dirty = $derived(!editing || !equals(WebhookSchema, w, baseline));
	const complete = $derived(w.name.trim() !== '' && w.url.trim() !== '' && w.events.length > 0);
	const reachable = $derived(/^https?:\/\/[^\s/]/i.test(w.url.trim()));
	// What a sample posts, an answer standing only while the form still reads the same
	const sample = $derived(`${w.format} ${w.events[0] ?? 0} ${w.url.trim()}`);
	let saving = $state(false);
	let testing = $state(false);
	let error = $state<string | undefined>(undefined);
	let answer = $state<{ sample: string; status: number; error?: string } | undefined>(undefined);
	const shown = $derived(answer?.sample === sample ? answer : undefined);
	const answered = $derived(shown !== undefined && shown.status >= 200 && shown.status < 300);
	let generation = $state(0);

	function discard() {
		if (!editing) {
			onclose();
			return;
		}
		w = clone(WebhookSchema, baseline);
		generation++;
	}

	async function save() {
		saving = true;
		error = undefined;
		try {
			await onsave(clone(WebhookSchema, w));
			onclose();
		} catch (err) {
			error = reason(err);
		} finally {
			saving = false;
		}
	}

	// Posts one sample to the URL in the format picked, saved or not
	async function test() {
		const sent = sample;
		testing = true;
		try {
			const res = await rpc.settings.testWebhook({ webhook: w });
			answer = { sample: sent, status: res.status };
		} catch (err) {
			answer = { sample: sent, status: 0, error: reason(err) };
		} finally {
			testing = false;
		}
	}
</script>

{#snippet enabled()}
	{#key generation}<Switch bind:checked={w.enabled} label="Enabled" compact />{/key}
{/snippet}

{#snippet sendTest()}
	<NeoButton text loading={testing} disabled={!reachable} onclick={test}>Send test</NeoButton>
{/snippet}

<PanelHeader title={draft ? draft.name : 'New webhook'} tools={enabled} {onclose} />
<Stack gap={16}>
	<FieldRow>
		<Text bind:value={w.name} label="Name" max={64} required />
		<Select bind:value={w.format} options={webhookFormats} label="Format" />
	</FieldRow>
	<Stack gap={4}>
		<Text bind:value={w.url} label="URL" type="url" mono required after={sendTest} />
		{#if shown}
			<div class="answer" aria-live="polite">
				<Dot lamp={answered ? 'success' : 'error'} />
				<span class="secondary" class:error-text={!answered}>{shown.error ?? `Answered ${shown.status}`}</span>
			</div>
		{/if}
	</Stack>
	{#key generation}
		<Checklist bind:value={w.events} options={webhookEvents} label="Events" />
	{/key}
</Stack>
<SaveBar {dirty} always={!editing} {saving} {error} label={editing ? 'Save' : 'Create'} disabled={!complete} onsave={save} ondiscard={discard} />

<style>
	.answer {
		display: flex;
		gap: var(--nebu-space-2);
		align-items: baseline;
		min-width: 0;
		overflow-wrap: anywhere;
	}
</style>
