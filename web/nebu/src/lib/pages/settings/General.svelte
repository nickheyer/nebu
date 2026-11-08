<script lang="ts">
	import { clone } from '@bufbuild/protobuf';
	import { useNeoThemeContext } from '#lib/neo/providers/neo-theme-provider-context.svelte.js';
	import { NeoTheme, NeoTransition } from '#lib/neo/providers/neo-theme-provider.model.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { live } from '#lib/live/events.svelte.js';
	import { SettingsSchema } from '#lib/proto/nebu/v1/settings_pb.js';
	import type { HeaderTab } from '#lib/shell/chrome.svelte.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import Card from '#lib/ui/layout/Card.svelte';
	import Columns from '#lib/ui/layout/Columns.svelte';
	import PageHeader from '#lib/ui/layout/PageHeader.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';

	let { tabs }: { tabs: HeaderTab[] } = $props();

	const theme = useNeoThemeContext();
	const stored = $derived(live.settings?.hostLabel ?? '');
	let hostLabel = $state('');
	let saving = $state(false);
	let error = $state<string | undefined>(undefined);
	const dirty = $derived(hostLabel.trim() !== stored);
	const themeChoice = $derived<'light' | 'dark' | 'system'>(theme.remember ? theme.theme : 'system');

	$effect(() => {
		hostLabel = stored;
	});

	async function save() {
		saving = true;
		error = undefined;
		try {
			const base = live.settings ?? (await rpc.settings.getSettings({})).settings;
			if (!base) throw new Error('the daemon answered without settings');
			const settings = clone(SettingsSchema, base);
			settings.hostLabel = hostLabel.trim();
			await rpc.settings.updateSettings({ settings });
			toast.success('Host label saved');
		} catch (err) {
			error = reason(err);
		} finally {
			saving = false;
		}
	}

	function discard() {
		hostLabel = stored;
		error = undefined;
	}

	// System follows the browser's scheme and forgets the stored choice
	function setTheme(v: 'light' | 'dark' | 'system') {
		if (v === 'system') {
			theme.update({ remember: false, theme: window.matchMedia('(prefers-color-scheme: dark)').matches ? NeoTheme.Dark : NeoTheme.Light });
			return;
		}
		theme.update({ remember: true, theme: v });
	}
</script>

<PageHeader {tabs} active="general" />

<Columns ratio="1/1">
	{#snippet left()}
		<Card title="Host">
			<Text bind:value={hostLabel} label="Host label" placeholder={live.host?.hostname} info="Names this host in the top bar and the overview, the hostname when empty" />
			<SaveBar {dirty} {saving} {error} onsave={save} ondiscard={discard} />
		</Card>
	{/snippet}
	{#snippet right()}
		<Card title="Appearance" info="Applies at once and is remembered in this browser">
			<FieldRow>
				<Segmented value={themeChoice} options={[{ value: 'light', label: 'Light' }, { value: 'dark', label: 'Dark' }, { value: 'system', label: 'System' }]} label="Theme" onchange={setTheme} />
				<Stack gap={12}>
					<Switch checked={theme.transition !== NeoTransition.None} label="Transitions" info="Animates the switch between light and dark" onchange={(on) => theme.update({ transition: on ? NeoTransition.Circle : NeoTransition.None })} />
					<Switch checked={theme.reset} label="Style reset" info="Normalises browser default styles under the theme" onchange={(on) => theme.update({ reset: on })} />
				</Stack>
			</FieldRow>
		</Card>
	{/snippet}
</Columns>
