<script lang="ts">
	import { goto } from '$app/navigation';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { enumLabel, num } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { BotStateSchema, type Bot, type ProbeBotTokenResponse } from '#lib/proto/nebu/v1/bot_pb.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Dialog from '#lib/ui/Dialog.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import RowAction from '#lib/ui/RowAction.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Secret from '#lib/ui/fields/Secret.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import Cell from '#lib/ui/layout/Cell.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import Row from '#lib/ui/layout/Row.svelte';
	import { botLamp } from '#lib/ui/status.js';
	import Avatar from '#lib/pages/bots/Avatar.svelte';
	import { botTip, running } from '#lib/pages/bots/bots.js';
	import PlusIcon from 'phosphor-svelte/lib/PlusIcon';
	import RobotIcon from 'phosphor-svelte/lib/RobotIcon';

	const list = new ListQuery<Bot, Record<string, never>>(async (_p, _cursor, _limit, signal) => {
		const res = await rpc.bots.listBots({}, { signal });
		return { items: res.bots, next: '' };
	}, [EventKind.BOT]);
	void list.load({});

	const bots = $derived(list.items.map((b) => live.bots.get(b.id) ?? b).sort((a, b) => a.name.localeCompare(b.name)));

	let open = $state(false);
	let name = $state('');
	let token = $state('');
	let enabled = $state(true);
	let probed = $state<ProbeBotTokenResponse | undefined>(undefined);
	let probing = $state(false);
	let creating = $state(false);

	// A new token clears the probe of the old one
	$effect(() => {
		void token;
		probed = undefined;
	});

	function openNew() {
		name = '';
		token = '';
		enabled = true;
		open = true;
	}

	async function probe() {
		probing = true;
		try {
			probed = await rpc.bots.probeBotToken({ token: token.trim() });
		} catch (err) {
			probed = undefined;
			toast.error('Token check failed', reason(err));
		} finally {
			probing = false;
		}
	}

	async function createBot() {
		creating = true;
		try {
			const res = await rpc.bots.createBot({ name: name.trim(), token: token.trim(), enabled });
			toast.success(`Created ${name.trim()}`);
			open = false;
			if (res.bot) void goto(`/bots/${res.bot.id}/status`);
		} catch (err) {
			toast.error('Create failed', reason(err));
		} finally {
			creating = false;
		}
	}

	async function start(b: Bot) {
		try {
			await rpc.bots.startBot({ id: b.id });
		} catch (err) {
			toast.error(`Start ${b.name} failed`, reason(err));
		}
	}

	async function stop(b: Bot) {
		try {
			await rpc.bots.stopBot({ id: b.id });
		} catch (err) {
			toast.error(`Stop ${b.name} failed`, reason(err));
		}
	}
</script>

<PageHeader />

<Card title="Bots" flush>
	{#snippet tools()}
		<NeoButton class="primary" elevation={2} onclick={openNew}>{#snippet icon()}<PlusIcon size={16} />{/snippet}Bot</NeoButton>
	{/snippet}
	{#if !list.settled}
		<Skeleton rows={3} leading="24px" secondary="12ch" menu={false} />
	{:else if list.error}
		<Empty text={list.error} action="Retry" onclick={() => list.refresh()} />
	{:else if bots.length === 0}
		<Empty icon={RobotIcon} text="No bots" action="Bot" onclick={openNew} />
	{:else}
		<List head={{ primary: 'Bot', secondary: 'State', facts: ['Servers'] }}>
			{#each bots as b (b.id)}
				{@const on = running(b)}
				<ListRow
					lamp={botLamp(b.state)}
					shape={on ? 'filled' : 'hollow'}
					primary={b.name}
					primaryTip={botTip(b)}
					secondary={enumLabel(BotStateSchema, b.state)}
					secondaryWidth="12ch"
					facts={[{ value: on ? num(b.status?.guilds) : '' }]}
					onclick={() => goto(`/bots/${b.id}/status`)}
				>
					{#snippet leading()}<Avatar url={b.status?.avatarUrl} size={24} />{/snippet}
					{#snippet action()}
						{#if on}
							<Confirm label="Stop" question="Stop {b.name}?" danger onconfirm={() => stop(b)} />
						{:else}
							<RowAction onclick={() => start(b)} label="Start" />
						{/if}
					{/snippet}
				</ListRow>
			{/each}
		</List>
	{/if}
</Card>

<Dialog bind:open title="New bot" confirm="Create" busy={creating} disabled={!name.trim() || !token.trim()} onconfirm={createBot}>
	<Text bind:value={name} label="Name" max={64} required />
	<Row gap={8} align="flex-end" wrap={false}>
		<Cell min="10rem"><Secret bind:value={token} label="Discord bot token" required /></Cell>
		<span class="trail"><NeoButton elevation={2} loading={probing} disabled={!token.trim()} onclick={probe}>Check</NeoButton></span>
	</Row>
	{#if probed}
		<Row gap={12} align="flex-start" wrap={false}>
			<Avatar url={probed.avatarUrl} size={40} />
			<Cell min="10rem">
				<KeyRows
					dense
					rows={[
						{ label: 'username', value: probed.username },
						{ label: 'invite', value: probed.inviteUrl, href: probed.inviteUrl, copy: true }
					]}
				/>
			</Cell>
		</Row>
	{/if}
	<Switch bind:checked={enabled} label="Start it now" />
</Dialog>
