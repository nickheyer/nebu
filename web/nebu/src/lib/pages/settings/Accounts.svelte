<script lang="ts">
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import type { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { session } from '#lib/auth/session.svelte.js';
	import { ago, when } from '#lib/format.js';
	import { Role, type User } from '#lib/proto/nebu/v1/auth_pb.js';
	import { SignInSchema } from '#lib/proto/nebu/v1/settings_pb.js';
	import type { HeaderTab } from '#lib/shell/chrome.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Chips from '#lib/ui/fields/Chips.svelte';
	import Duration from '#lib/ui/fields/Duration.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import Columns from '#lib/ui/layout/Columns.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import { SectionDraft, SettingsForm } from '#lib/pages/settings/form.svelte.js';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import UsersIcon from 'phosphor-svelte/lib/UsersIcon';

	let { tabs, users, off }: { tabs: HeaderTab[]; users: ListQuery<User, Record<string, never>>; off: boolean } = $props();

	const roles = [
		{ value: Role.ADMIN, label: 'Admin' },
		{ value: Role.OPERATOR, label: 'Operator' },
		{ value: Role.INFERENCE, label: 'Inference' }
	];
	const sso = $derived(session.status?.sso?.name ?? '');
	const auth = $derived(session.status?.enabled === true);
	const form = new SettingsForm();
	const signIn = new SectionDraft(form, SignInSchema, (s) => s.signIn, (s, v) => (s.signIn = v), 'Sign in saved');
	// Redraws the role boxes after a refused change so each shows the role its account kept
	let roleFailures = $state(0);

	let addOpen = $state(false);
	let username = $state('');
	let password = $state('');
	let role = $state<Role>(Role.OPERATOR);
	let addBusy = $state(false);
	let addError = $state('');

	let passwordOpen = $state(false);
	let target = $state<User | undefined>(undefined);
	let fresh = $state('');
	let passwordBusy = $state(false);
	let passwordError = $state('');

	function openAdd() {
		username = '';
		password = '';
		role = Role.OPERATOR;
		addError = '';
		addOpen = true;
	}

	async function add() {
		addBusy = true;
		addError = '';
		try {
			await rpc.auth.createUser({ username: username.trim(), password, role });
			toast.success(`Created ${username.trim()}`);
			addOpen = false;
			await users.load({});
		} catch (err) {
			addError = reason(err);
		} finally {
			addBusy = false;
		}
	}

	function openPassword(u: User) {
		target = u;
		fresh = '';
		passwordError = '';
		passwordOpen = true;
	}

	async function setPassword() {
		if (!target) return;
		passwordBusy = true;
		passwordError = '';
		try {
			await rpc.auth.setPassword({ username: target.username, password: fresh, currentPassword: '' });
			toast.success(`Password set for ${target.username}`);
			passwordOpen = false;
		} catch (err) {
			passwordError = reason(err);
		} finally {
			passwordBusy = false;
		}
	}

	// Gives the account the role, which ends its sessions, the caller's own among them
	async function setRole(u: User, next: Role | undefined) {
		if (next === undefined || next === u.role) return;
		try {
			await rpc.auth.setRole({ username: u.username, role: next });
			toast.success(`${u.username} is now ${roles.find((r) => r.value === next)?.label.toLowerCase()}`);
			if (own(u)) await session.load();
			else await users.load({});
		} catch (err) {
			roleFailures++;
			toast.error('Role change failed', reason(err));
		}
	}

	async function remove(u: User) {
		try {
			await rpc.auth.deleteUser({ username: u.username });
			toast.success(`Deleted ${u.username}`);
			await users.load({});
		} catch (err) {
			toast.error('Delete failed', reason(err));
		}
	}

	function own(u: User): boolean {
		return u.username === session.user?.name || u.username === session.user?.subject;
	}
</script>

<PageHeader {tabs} active="accounts" />

{#snippet accountsCard()}
	<Card title="Accounts" flush>
		{#snippet tools()}
			{#if !off}<NeoButton class="primary" elevation={2} onclick={openAdd}>Add account</NeoButton>{/if}
		{/snippet}
		{#if off}
			<Empty icon={UsersIcon} text={sso ? `Managed by ${sso}` : 'Authentication off'} />
		{:else if !users.settled}
			<Skeleton rows={3} dot={false} secondary="10ch" facts={1} action={false} />
		{:else if users.error}
			<Empty text={users.error} action="Retry" onclick={() => users.refresh()} />
		{:else if users.items.length === 0}
			<Empty icon={UsersIcon} text="No accounts" action="Add account" onclick={openAdd} />
		{:else}
			<List head={{ primary: 'Username', facts: ['Created'], action: 'Role' }}>
				{#each users.items as u (u.id)}
					<ListRow
						primary={u.username}
						facts={[{ value: ago(u.createdAt, clockTick.now), tip: when(u.createdAt) }]}
						menu={[
							{ label: 'Set password', onclick: () => openPassword(u) },
							{ label: 'Delete', danger: true, divider: true, disabled: own(u), description: own(u) ? 'signed in' : undefined, confirm: `Delete ${u.username}?`, onclick: () => remove(u) }
						]}
					>
						{#snippet action()}
							{#key `${u.role}:${roleFailures}`}<Select value={u.role} options={roles} fit compact label="Role" onchange={(r) => setRole(u, r)} />{/key}
						{/snippet}
					</ListRow>
				{/each}
			</List>
			<LoadMore next={users.next} loading={users.loading} onmore={() => users.more()} />
		{/if}
	</Card>
{/snippet}

{#if auth}
	<Columns ratio="3/2">
		{#snippet left()}{@render accountsCard()}{/snippet}
		{#snippet right()}
			<Card title="Sign in">
				{#if form.error && !form.ready}
					<Empty text="Settings failed to load" hint={form.error} action="Retry" onclick={() => form.load()} />
				{:else if !form.ready}
					<Skeleton rows={2} kind="facts" />
				{:else}
					<Duration bind:value={signIn.draft.sessionSeconds} base="s" smallest="min" max={31_536_000} label="Session lifetime" info="How long a browser stays signed in" />
					<Chips bind:value={signIn.draft.trustedProxies} label="Trusted proxies" info="Networks as CIDRs whose forwarded address, scheme and host headers are believed" />
					<SaveBar dirty={signIn.dirty} saving={signIn.saving} error={signIn.error} onsave={() => signIn.save()} ondiscard={() => signIn.discard()} />
				{/if}
			</Card>
		{/snippet}
	</Columns>
{:else}
	{@render accountsCard()}
{/if}

<Dialog bind:open={addOpen} title="New account" confirm="Create" busy={addBusy} disabled={!username.trim() || !password} onconfirm={add}>
	<Text bind:value={username} label="Username" max={64} required autocomplete="off" />
	<Secret bind:value={password} label="Password" required autocomplete="new-password" />
	<Segmented bind:value={role} options={roles} label="Role" info="Admin does everything, operator runs and edits, inference only sends requests" />
	{#snippet footer()}
		{#if addError}<span class="error-text">{addError}</span>{/if}
	{/snippet}
</Dialog>

<Dialog bind:open={passwordOpen} title="Set password" confirm="Set" busy={passwordBusy} disabled={!fresh} onconfirm={setPassword}>
	{#if target}<Text value={target.username} label="Username" readonly />{/if}
	<Secret bind:value={fresh} label="New password" required autocomplete="new-password" />
	{#snippet footer()}
		{#if passwordError}<span class="error-text">{passwordError}</span>{/if}
	{/snippet}
</Dialog>
