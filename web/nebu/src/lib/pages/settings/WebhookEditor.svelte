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
	import PaperPlaneTiltIcon from 'phosphor-svelte/lib/PaperPlaneTiltIcon';

	// One webhook being added or changed, written back through the list that holds it
	let { webhook, onsave, onclose }: { webhook?: Webhook; onsave: (w: Webhook) => Promise<void>; onclose: () => void } = $props();

	const draft = untrack(() => webhook);
	const editing = draft !== undefined;
	let w = $state(draft ? clone(WebhookSchema, draft) : create(WebhookSchema, { enabled: true, format: WebhookFormat.EVENT, events: [NotifyEvent.MODEL_FAILED, NotifyEvent.TASK_FAILED] }));
	const baseline = untrack(() => clone(WebhookSchema, w));
	const dirty = $derived(!editing || !equals(WebhookSchema, w, baseline));
	// What the webhook still needs, the first thing missing
	const missing = $derived(w.name.trim() === '' ? 'Name the webhook' : w.url.trim() === '' ? 'Give it a URL' : w.events.length === 0 ? 'Pick at least one event' : '');
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

<PanelHeader title={draft ? draft.name : 'New webhook'} {onclose} />
<Stack gap={16}>
	<FieldRow>
		<Text bind:value={w.name} label="Name" max={64} required />
		<Select bind:value={w.format} options={webhookFormats} label="Format" />
	</FieldRow>
	<Stack gap={8}>
		<Text bind:value={w.url} label="URL" type="url" mono required />
		<div class="test">
			<NeoButton elevation={2} loading={testing} disabled={!reachable} onclick={test}>{#snippet icon()}<PaperPlaneTiltIcon size={16} />{/snippet}Send a test message</NeoButton>
			{#if shown}
				<span class="answer" aria-live="polite">
					<Dot lamp={answered ? 'success' : 'error'} />
					<span class="secondary" class:error-text={!answered}>{shown.error ?? `Answered ${shown.status}`}</span>
				</span>
			{:else if !reachable}
				<span class="secondary">Once the URL is filled in</span>
			{/if}
		</div>
	</Stack>
	{#key generation}
		<Checklist bind:value={w.events} options={webhookEvents} label="Send a message when" />
		<Switch bind:checked={w.enabled} label="Enabled" info="A webhook turned off is sent nothing" />
	{/key}
</Stack>
<SaveBar {dirty} always={!editing} {saving} {error} label={editing ? 'Save' : 'Create'} needs={missing} onsave={save} ondiscard={discard} />

<style>
	.test {
		display: flex;
		flex-wrap: wrap;
		gap: var(--nebu-space-2) var(--nebu-space-3);
		align-items: center;
		min-width: 0;
	}
	.answer {
		display: inline-flex;
		gap: var(--nebu-space-2);
		align-items: baseline;
		min-width: 0;
		overflow-wrap: anywhere;
	}
</style>
