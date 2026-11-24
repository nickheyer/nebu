<script lang="ts">
	import { clone, create } from '@bufbuild/protobuf';
	import { onMount } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { ago, num, when } from '#lib/format.js';
	import { BudgetPeriod, BudgetUnit, CredentialBudgetSchema, CredentialKind, WindowKind, type CredentialBudget, type CredentialWindow, type ProviderCredential, type ProviderSignIn } from '#lib/proto/nebu/v1/auth_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { catalog } from '#lib/state/catalog.svelte.js';
	import CopyButton from '#lib/ui/CopyButton.svelte';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import Screen from '#lib/ui/Screen.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import StateWord from '#lib/ui/StateWord.svelte';
	import Bar from '#lib/ui/Bar.svelte';
	import Chips from '#lib/ui/fields/Chips.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import NumberField from '#lib/ui/fields/Number.svelte';
	import Percent from '#lib/ui/fields/Percent.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import PlugsConnectedIcon from 'phosphor-svelte/lib/PlugsConnectedIcon';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import SignInIcon from 'phosphor-svelte/lib/SignInIcon';

	// The caller's own credentials for providers serving models
	const credentials = new ListQuery<ProviderCredential, Record<string, never>>(async (_p, cursor, limit, signal) => {
		const res = await rpc.auth.listCredentials({ cursor, limit }, { signal });
		return { items: res.credentials, next: res.nextCursor };
	}, []);

	onMount(() => {
		void credentials.load({});
		void catalog.load();
	});

	// The sources taking each kind of credential
	const providers = $derived(catalog.sources.filter((s) => s.capabilities?.credentials.length));
	const taking = (kind: CredentialKind) => providers.filter((s) => s.capabilities?.credentials.includes(kind)).map((s) => ({ value: s.source?.id ?? '', label: s.source?.name || s.source?.id || '' }));
	const keyed = $derived(taking(CredentialKind.KEY));
	const signed = $derived(taking(CredentialKind.SIGN_IN));
	const sourceName = (id: string) => providers.find((s) => s.source?.id === id)?.source?.name || id;

	let keyOpen = $state(false);
	// The credential whose key is replaced, none for a new one
	let editing = $state<ProviderCredential | undefined>(undefined);
	let sourceId = $state('');
	let name = $state('');
	let secret = $state('');
	let busy = $state(false);
	let error = $state('');

	function openKey(c?: ProviderCredential) {
		editing = c;
		sourceId = c?.sourceId ?? keyed[0]?.value ?? '';
		name = c?.name ?? '';
		secret = '';
		error = '';
		keyOpen = true;
	}

	// Lists again, and the sources, whose credential state follows
	async function changed() {
		await credentials.load({});
		void catalog.refresh();
	}

	async function saveKey() {
		busy = true;
		error = '';
		try {
			await rpc.auth.putCredential({ id: editing?.id ?? '', sourceId: editing ? '' : sourceId, name: name.trim(), secret: secret.trim() });
			keyOpen = false;
			toast.success(editing ? `Updated ${name || editing.name}` : `Key kept for ${sourceName(sourceId)}`);
			await changed();
		} catch (err) {
			error = reason(err);
		} finally {
			busy = false;
			secret = '';
		}
	}

	async function remove(c: ProviderCredential) {
		try {
			const res = await rpc.auth.deleteCredential({ id: c.id });
			if (res.revocationUnconfirmed) toast.warning(`Removed ${c.name}`, `${sourceName(c.sourceId)} has yet to confirm ending its session. nebu keeps asking, disconnect nebu there to end it now`);
			else toast.success(`Removed ${c.name}`);
			await changed();
		} catch (err) {
			toast.error('Remove failed', reason(err));
		}
	}

	let signOpen = $state(false);
	let pending = $state<ProviderSignIn | undefined>(undefined);
	// The address the browser landed on after approving, copied back for a sign-in by redirect
	let landed = $state('');
	let waiting: AbortController | undefined;
	// The sign-in being authorized again in place, undefined for a new one
	let again = $state<ProviderCredential | undefined>(undefined);

	function openSignIn(c?: ProviderCredential) {
		again = c;
		sourceId = c?.sourceId ?? signed[0]?.value ?? '';
		name = '';
		error = '';
		landed = '';
		pending = undefined;
		signOpen = true;
	}

	// Starts the sign-in, then for a device code waits for the person to approve it
	async function signIn() {
		if (pending) return finish(landed.trim());
		busy = true;
		error = '';
		try {
			const res = await rpc.auth.startSignIn({ sourceId, name: name.trim(), credentialId: again?.id ?? '' });
			pending = res.signIn;
			if (pending?.code) await finish('');
		} catch (err) {
			error = reason(err);
		} finally {
			busy = false;
		}
	}

	async function finish(callback: string) {
		busy = true;
		error = '';
		waiting = new AbortController();
		try {
			await rpc.auth.finishSignIn({ id: pending?.id ?? '', callback }, { signal: waiting.signal });
			signOpen = false;
			toast.success(`Signed in to ${sourceName(sourceId)}`);
			await changed();
		} catch (err) {
			if (!waiting.signal.aborted) error = reason(err);
		} finally {
			busy = false;
		}
	}

	function closeSignIn() {
		waiting?.abort();
		pending = undefined;
	}

	// The credentials kept for the same source, in position order
	const siblings = (c: ProviderCredential) => credentials.items.filter((o) => o.sourceId === c.sourceId);

	async function move(c: ProviderCredential, by: number) {
		try {
			// Its place among its source's credentials, which stored positions may leave gaps in
			await rpc.auth.putCredential({ id: c.id, position: siblings(c).findIndex((o) => o.id === c.id) + 1 + by });
			await credentials.load({});
		} catch (err) {
			toast.error('Reorder failed', reason(err));
		}
	}

	const units = [
		{ value: BudgetUnit.TOKENS, label: 'Tokens' },
		{ value: BudgetUnit.INPUT_TOKENS, label: 'Input tokens' },
		{ value: BudgetUnit.OUTPUT_TOKENS, label: 'Output tokens' },
		{ value: BudgetUnit.REQUESTS, label: 'Requests' },
		{ value: BudgetUnit.PERCENT_USED, label: 'Percent used' }
	];
	const periods = [
		{ value: BudgetPeriod.ROLLING, label: 'Rolling' },
		{ value: BudgetPeriod.DAY, label: 'Day' },
		{ value: BudgetPeriod.WEEK, label: 'Week' },
		{ value: BudgetPeriod.MONTH, label: 'Month' },
		{ value: BudgetPeriod.REPORTED, label: 'Reported window' }
	];
	const starts: Partial<Record<BudgetPeriod, { label: string; max: number }>> = {
		[BudgetPeriod.DAY]: { label: 'Starts at hour', max: 23 },
		[BudgetPeriod.WEEK]: { label: 'Starts on weekday', max: 6 },
		[BudgetPeriod.MONTH]: { label: 'Starts on day', max: 28 }
	};

	let budgetsOpen = $state(false);
	let budgeting = $state<ProviderCredential | undefined>(undefined);
	let budgets = $state<CredentialBudget[]>([]);

	function openBudgets(c: ProviderCredential) {
		budgeting = c;
		budgets = c.budgets.map((b) => clone(CredentialBudgetSchema, b));
		error = '';
		budgetsOpen = true;
	}

	function addBudget() {
		budgets = [...budgets, create(CredentialBudgetSchema, { name: `budget ${budgets.length + 1}`, unit: BudgetUnit.TOKENS, period: BudgetPeriod.DAY, limit: 1_000_000 })];
	}

	// Whether a rolling budget spans less than the five minutes usage is counted in or more than the hundred days it is kept
	const rollsOutside = (b: CredentialBudget) => b.unit !== BudgetUnit.PERCENT_USED && b.period === BudgetPeriod.ROLLING && (b.seconds < 300 || b.seconds > 8_640_000);

	async function saveBudgets() {
		busy = true;
		error = '';
		try {
			await rpc.auth.putCredential({ id: budgeting?.id ?? '', budgets, budgetsSet: true });
			budgetsOpen = false;
			toast.success(`Budgets saved for ${budgeting?.name}`);
			await credentials.load({});
		} catch (err) {
			error = reason(err);
		} finally {
			busy = false;
		}
	}

	// A window's reading, a cooldown having nothing but its end
	function reading(w: CredentialWindow): string {
		if (w.kind === WindowKind.COOLDOWN) return 'cooling down';
		return `${num(Math.round(w.used))} / ${num(Math.round(w.limit))}`;
	}
</script>

{#snippet windows(c: ProviderCredential)}
	<div class="windows">
		{#each c.windows as w (w.name + w.kind)}
			<span class="tag">{w.name}</span>
			<span class="track"><Bar segments={[{ kind: w.spent ? 'warning' : 'used', label: 'used', bytes: w.kind === WindowKind.COOLDOWN ? 1 : w.used }]} total={w.kind === WindowKind.COOLDOWN ? 1 : w.limit} height={6} format={(n) => num(Math.round(n))} /></span>
			<span class="used">{reading(w)}</span>
			<span class="reset secondary">{w.resetsAt ? `resets ${ago(w.resetsAt, clockTick.now)}` : ''}</span>
		{/each}
	</div>
{/snippet}

<Card title="Providers" flush>
	{#snippet tools()}
		{#if signed.length}<NeoButton elevation={2} onclick={() => openSignIn()}>{#snippet icon()}<SignInIcon size={16} />{/snippet}Sign in</NeoButton>{/if}
		{#if keyed.length}<NeoButton class="primary" elevation={2} onclick={() => openKey()}>{#snippet icon()}<PlusIcon size={16} />{/snippet}Key</NeoButton>{/if}
	{/snippet}
	{#if !credentials.settled}
		<Skeleton rows={2} dot={false} secondary="10ch" action={false} />
	{:else if credentials.error}
		<Empty text={credentials.error} action="Retry" onclick={() => credentials.refresh()} />
	{:else if credentials.items.length === 0}
		<Empty icon={PlugsConnectedIcon} text="No credentials" hint="Requests to a provider's models carry your own" />
	{:else}
		<List head={{ primary: 'Name', secondary: 'Source', instrument: 'Kind', facts: ['Expires'] }}>
			{#each credentials.items as c (c.id)}
				<ListRow
					primary={c.name}
					secondary={sourceName(c.sourceId)}
					secondaryWidth="16ch"
					facts={[c.expiresAt ? { value: ago(c.expiresAt, clockTick.now), tip: `access lapses ${when(c.expiresAt)}, refreshed before` } : { value: '', tip: `updated ${when(c.updatedAt)}` }]}
					menu={[
						...(c.kind === CredentialKind.KEY ? [{ label: 'Replace key', onclick: () => openKey(c) }] : [{ label: 'Sign in again', onclick: () => openSignIn(c) }]),
						{ label: 'Budgets', onclick: () => openBudgets(c) },
						...(siblings(c)[0]?.id !== c.id ? [{ label: 'Move up', onclick: () => move(c, -1) }] : []),
						...(siblings(c).at(-1)?.id !== c.id ? [{ label: 'Move down', onclick: () => move(c, 1) }] : []),
						{ label: 'Remove', danger: true, confirm: `Remove ${c.name}?`, onclick: () => remove(c) }
					]}
				>
					{#snippet instrument()}<span class="secondary">{c.kind === CredentialKind.SIGN_IN ? 'Sign-in' : 'Key'}{c.position > 1 ? ` · ${c.position}` : ''}</span>{/snippet}
					{#snippet badges()}{#if c.windows.some((w) => w.spent)}<StateWord label="spent" lamp="warning" />{/if}{/snippet}
					{#snippet below()}{#if c.windows.length}{@render windows(c)}{/if}{/snippet}
				</ListRow>
			{/each}
		</List>
		<LoadMore next={credentials.next} loading={credentials.loading} onmore={() => credentials.more()} />
	{/if}
</Card>

<Dialog bind:open={keyOpen} title={editing ? 'Replace key' : 'New key'} confirm="Save" {busy} needs={!editing && !sourceId ? 'Pick a source' : !secret.trim() ? 'Enter the key' : ''} onconfirm={saveKey} onclose={() => (secret = '')}>
	{#if !editing}<Select bind:value={sourceId} options={keyed} label="Source" required />{/if}
	<Text bind:value={name} label="Name" max={64} placeholder={editing ? editing.name : sourceName(sourceId)} />
	<Secret bind:value={secret} label="Key" required />
	{#snippet footer()}
		{#if error}<span class="error-text">{error}</span>{/if}
	{/snippet}
</Dialog>

<Dialog bind:open={budgetsOpen} title={`Budgets of ${budgeting?.name ?? ''}`} confirm="Save" {busy} needs={budgets.some((b) => !b.name.trim()) ? 'Name every budget' : budgets.some((b) => !(b.limit > 0)) ? 'Give every budget a limit' : budgets.some(rollsOutside) ? 'A rolling budget spans 5 minutes to 100 days' : ''} onconfirm={saveBudgets} onclose={() => (budgeting = undefined)}>
	{#each budgets as b, i (i)}
		<div class="budget">
			<FieldRow>
				<Text bind:value={b.name} label="Name" max={64} required />
				<NumberField bind:value={b.limit} label={b.unit === BudgetUnit.PERCENT_USED ? 'Limit, percent used' : 'Limit'} info={b.unit === BudgetUnit.PERCENT_USED ? 'Spent once this much of the reported window is used' : undefined} min={0} fraction />
			</FieldRow>
			<FieldRow>
				<Select bind:value={b.unit} options={units} label="Unit" />
				{#if b.unit !== BudgetUnit.PERCENT_USED}<Select bind:value={b.period} options={periods} label="Period" />{/if}
			</FieldRow>
			<Chips bind:value={b.models} label="Models" placeholder="gpt-4*" unset="every model" />
			{#if b.unit === BudgetUnit.PERCENT_USED || b.period === BudgetPeriod.REPORTED}
				<Text bind:value={b.window} label="Reported window" placeholder="tokens" max={64} required />
			{:else if b.period === BudgetPeriod.ROLLING}
				<Duration bind:value={b.seconds} base="s" label="Over" info="5 minutes to 100 days" />
			{:else if starts[b.period]}
				<FieldRow>
					<NumberField bind:value={b.start} label={starts[b.period]?.label} min={0} max={starts[b.period]?.max} />
					<Text bind:value={b.zone} label="Zone" placeholder="UTC" max={64} />
				</FieldRow>
			{/if}
			<FieldRow>
				<Percent bind:value={b.reservePercent} label="Reserve" />
				<Switch bind:checked={b.cached} label="Cached input counts" />
			</FieldRow>
			<span class="remove"><NeoButton text class="danger" onclick={() => (budgets = budgets.filter((_, j) => j !== i))}>Remove budget</NeoButton></span>
		</div>
	{/each}
	<span class="remove"><NeoButton text onclick={addBudget}>{#snippet icon()}<PlusIcon size={18} />{/snippet}Budget</NeoButton></span>
	{#snippet footer()}
		{#if error}<span class="error-text">{error}</span>{/if}
	{/snippet}
</Dialog>

<Dialog bind:open={signOpen} title={again ? `Sign in again, ${again.name}` : 'Sign in'} confirm={pending?.code ? undefined : pending ? 'Finish' : 'Continue'} {busy} needs={!sourceId ? 'Pick a source' : pending && !pending.code && !landed.trim() ? 'Paste the address' : ''} onconfirm={signIn} onclose={closeSignIn}>
	{#if pending?.code}
		<p class="secondary">Open the page, enter the code, and approve</p>
		<a href={pending.url} target="_blank" rel="noreferrer">{pending.url}</a>
		<div class="code">
			<Screen padding={8}><span class="value">{pending.code}</span></Screen>
			<CopyButton text={pending.code} />
		</div>
	{:else if pending}
		<p class="secondary">Open the page and approve, then paste the address it lands on, or the code it shows</p>
		<a href={pending.url} target="_blank" rel="noreferrer">Open sign-in page</a>
		<Text bind:value={landed} label="Address or code" required />
	{:else if again}
		<p class="secondary">Signs in to {sourceName(sourceId)} again with the same account and replaces this sign-in's tokens, its budgets and place kept</p>
	{:else}
		<Select bind:value={sourceId} options={signed} label="Source" required />
		<Text bind:value={name} label="Name" max={64} placeholder={sourceName(sourceId)} />
	{/if}
	{#snippet footer()}
		{#if error}<span class="error-text">{error}</span>{/if}
	{/snippet}
</Dialog>

<style>
	.windows {
		display: grid;
		grid-template-columns: max-content minmax(5rem, 1fr) max-content max-content;
		gap: var(--nebu-space-1) var(--nebu-space-3);
		align-items: center;
		min-width: 0;
		font-size: var(--nebu-text-secondary);
	}
	.tag {
		font-weight: 500;
	}
	.track {
		display: flex;
		min-width: 0;
	}
	.used {
		font-variant-numeric: tabular-nums;
		text-align: right;
		white-space: nowrap;
	}
	.reset {
		white-space: nowrap;
	}
	.budget {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-3);
		padding-bottom: var(--nebu-space-3);
		border-bottom: 1px solid var(--nebu-border);
	}
	.remove {
		display: inline-flex;
		align-self: flex-start;
	}
	.code {
		display: flex;
		gap: var(--nebu-space-2);
		align-items: center;
		min-width: 0;
	}
	.code > :global(.screen) {
		flex: 1 1 auto;
	}
	.value {
		overflow-wrap: anywhere;
	}
</style>
