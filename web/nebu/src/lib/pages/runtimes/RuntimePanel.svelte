<script lang="ts">
	import { clone, create } from '@bufbuild/protobuf';
	import { untrack } from 'svelte';
	import { NeoButton } from '#lib/neo/buttons/index.js';
	import { ListQuery } from '#lib/api/list.svelte.js';
	import { reason, rpc } from '#lib/api/transport.js';
	import { capitalize, duration, enumLabel, factLabel, shortId, when } from '#lib/format.js';
	import { live } from '#lib/live/events.svelte.js';
	import { EventKind } from '#lib/proto/nebu/v1/event_pb.js';
	import { SandboxKind, SandboxKindSchema, type Build, type RecipeStatus, type Var } from '#lib/proto/nebu/v1/recipe_pb.js';
	import { InstallKind, type Install, type InstallOption, type RuntimeStatus } from '#lib/proto/nebu/v1/runtime_pb.js';
	import { RuntimeDefaultsSchema, SettingsSchema } from '#lib/proto/nebu/v1/settings_pb.js';
	import { toast } from '#lib/shell/notify.svelte.js';
	import { followTask } from '#lib/state/tasks.svelte.js';
	import Conditions from '#lib/ui/Conditions.svelte';
	import Confirm from '#lib/ui/Confirm.svelte';
	import Dot from '#lib/ui/Dot.svelte';
	import Empty from '#lib/ui/Empty.svelte';
	import KeyRows from '#lib/ui/KeyRows.svelte';
	import List from '#lib/ui/List.svelte';
	import ListRow from '#lib/ui/ListRow.svelte';
	import LoadMore from '#lib/ui/LoadMore.svelte';
	import PageTabs from '#lib/ui/PageTabs.svelte';
	import Skeleton from '#lib/ui/Skeleton.svelte';
	import Str from '#lib/ui/Str.svelte';
	import FieldRow from '#lib/ui/fields/FieldRow.svelte';
	import FormSection from '#lib/ui/fields/FormSection.svelte';
	import Path from '#lib/ui/fields/Path.svelte';
	import SaveBar from '#lib/ui/fields/SaveBar.svelte';
	import Segmented from '#lib/ui/fields/Segmented.svelte';
	import Select from '#lib/ui/fields/Select.svelte';
	import Switch from '#lib/ui/fields/Switch.svelte';
	import Text from '#lib/ui/fields/Text.svelte';
	import PanelHeader from '#lib/ui/layout/PanelHeader.svelte';
	import Stack from '#lib/ui/layout/Stack.svelte';
	import { reveal } from '#lib/ui/motion.svelte.js';
	import { clockTick } from '#lib/ui/clock.svelte.js';
	import { buildLamp } from '#lib/ui/status.js';
	import ConfigFields from '#lib/pages/runtimes/ConfigFields.svelte';
	import ParamsForm from '#lib/pages/serve/ParamsForm.svelte';
	import { installLabel } from '#lib/pages/serve/serve.js';
	import DownloadSimpleIcon from 'phosphor-svelte/lib/DownloadSimpleIcon';
	import HammerIcon from 'phosphor-svelte/lib/HammerIcon';
	import SlidersHorizontalIcon from 'phosphor-svelte/lib/SlidersHorizontalIcon';

	let { status, tab = 'installs', ontab, onclose }: { status: RuntimeStatus; tab?: string; ontab: (id: string) => void; onclose: () => void } = $props();

	const rt = $derived(status.runtime);
	const id = $derived(rt?.id ?? '');
	const installList = new ListQuery<Install, { runtimeId: string }>(
		async (p, cursor, limit, signal) => {
			const res = await rpc.runtimes.listInstalls({ runtimeId: p.runtimeId, cursor, limit }, { signal });
			return { items: res.installs, next: res.nextCursor };
		},
		[EventKind.INSTALL]
	);
	const buildList = new ListQuery<Build, { runtimeId: string }>(
		async (p, cursor, limit, signal) => {
			const res = await rpc.build.listBuilds({ runtimeId: p.runtimeId, cursor, limit }, { signal });
			return { items: res.builds, next: res.nextCursor };
		},
		[EventKind.BUILD]
	);
	const installs = $derived(installList.items.map((i) => live.installs.get(i.id) ?? i));
	const builds = $derived(buildList.items.map((b) => live.builds.get(b.id) ?? b));

	// The install form opens over the installs list under its own route
	const installing = $derived(tab === 'install');
	const shown = $derived(installing ? 'installs' : tab);

	$effect(() => {
		if (shown === 'installs') void installList.load({ runtimeId: id });
		else if (shown === 'builds') void buildList.load({ runtimeId: id });
	});
	const tabs = [
		{ id: 'installs', label: 'Installs' },
		{ id: 'builds', label: 'Builds' },
		{ id: 'defaults', label: 'Defaults' }
	];

	let method = $state(untrack(() => status.installs[0]?.method?.id ?? ''));
	const option = $derived(status.installs.find((o) => o.method?.id === method));
	const buildMethod = $derived(status.installs.find((o) => o.method?.kind === InstallKind.BUILT)?.method?.id);
	let adoptPath = $state('');
	let settings = $state<Record<string, string>>({});
	let recipes = $state<RecipeStatus[]>([]);
	let recipesAsked = $state(false);
	let variant = $state('');
	let vars = $state<Record<string, string>>({});
	let sandbox = $state<SandboxKind>(SandboxKind.HOST);
	let image = $state('');
	let ref = $state('');
	let force = $state(false);
	// Recipe patches the build leaves out, every patch applied until switched off
	let skipPatches = $state<string[]>([]);
	let busy = $state(false);

	const recipe = $derived(option?.recipe ?? recipes.find((r) => r.recipe?.runtimeId === id));
	const variants = $derived((recipe?.recipe?.variants ?? []).map((v) => ({ value: v.id, label: v.id, description: unsupported(v) ? v.requires : v.description, disabled: unsupported(v) })));
	const absent = $derived(recipe?.absentTools ?? []);
	const chosen = $derived(recipe?.recipe?.variants.find((v) => v.id === variant));
	const patches = $derived(recipe?.recipe?.patches ?? []);
	// Tools the chosen variant needs that this host lacks, none inside a container which brings its own
	const missing = $derived(sandbox === SandboxKind.HOST ? [...(recipe?.recipe?.tools ?? []), ...(chosen?.tools ?? [])].filter((t) => absent.includes(t)) : []);

	// A variant with requirements this host lacks
	function unsupported(v: { vars: Record<string, string>; requires: string }): boolean {
		return Object.keys(v.vars).length === 0 && v.requires !== '';
	}

	// Names the lacking tools in a sentence, such as hipcc and cmake
	function missingText(tools: string[]): string {
		const list = tools.length > 1 ? `${tools.slice(0, -1).join(', ')} and ${tools[tools.length - 1]}` : tools[0];
		return `Needs ${list}, which this host lacks`;
	}

	type BuildField = { kind: 'variant' | 'sandbox' | 'image' | 'ref' | 'force' } | { kind: 'var'; v: Var };
	const buildFields = $derived<BuildField[]>([
		{ kind: 'variant' },
		{ kind: 'sandbox' },
		...(sandbox === SandboxKind.OCI ? [{ kind: 'image' as const }] : []),
		{ kind: 'ref' },
		{ kind: 'force' },
		...(recipe?.recipe?.vars ?? []).map((v) => ({ kind: 'var' as const, v }))
	]);

	// Asks for the recipe of a build method the runtime status carries none for
	$effect(() => {
		if (!installing || recipesAsked || option?.method?.kind !== InstallKind.BUILT || option.recipe) return;
		recipesAsked = true;
		void rpc.build
			.listRecipes({ runtimeId: id })
			.then((r) => (recipes = r.recipes))
			.catch((err) => toast.error('Recipes failed to list', reason(err)));
	});

	$effect(() => {
		if (!variant && recipe) {
			variant = recipe.variant || recipe.recipe?.variants[0]?.id || '';
			sandbox = recipe.sandbox || SandboxKind.HOST;
			vars = { ...recipe.vars };
		}
	});

	function methodLabel(o: InstallOption): string {
		const k = o.method?.kind;
		const word = k === InstallKind.ADOPTED ? 'Use existing' : k === InstallKind.BUILT ? 'Build' : 'Download';
		const shared = status.installs.filter((x) => x.method?.kind === k).length > 1;
		return shared ? capitalize(o.method?.id ?? word) : word;
	}
	const methods = $derived(status.installs.map((o) => ({ value: o.method?.id ?? '', label: methodLabel(o), tip: o.method?.description || undefined })));
	const verb = $derived(option?.method?.kind === InstallKind.ADOPTED ? 'Add' : option?.method?.kind === InstallKind.BUILT ? 'Build' : 'Install');

	function pairs<T>(list: T[]): T[][] {
		const out: T[][] = [];
		for (let i = 0; i < list.length; i += 2) out.push(list.slice(i, i + 2));
		return out;
	}

	function togglePatch(id: string, on: boolean) {
		skipPatches = on ? skipPatches.filter((x) => x !== id) : [...skipPatches.filter((x) => x !== id), id];
	}

	function commit() {
		const kind = option?.method?.kind;
		if (kind === InstallKind.ADOPTED) return adopt();
		if (kind === InstallKind.BUILT) return build();
		return install();
	}

	async function adopt() {
		busy = true;
		try {
			await rpc.runtimes.adoptInstall({ runtimeId: id, path: adoptPath });
			toast.success(`Added ${adoptPath || rt?.name}`);
			adoptPath = '';
			ontab('installs');
		} catch (err) {
			toast.error('Add failed', reason(err));
		} finally {
			busy = false;
		}
	}

	async function install() {
		busy = true;
		try {
			const res = await rpc.runtimes.install({ runtimeId: id, method, settings });
			if (!res.task) throw new Error('the daemon answered without a task');
			followTask(res.task, `Install ${rt?.name}`);
			ontab('installs');
		} catch (err) {
			toast.error('Install failed', reason(err));
		} finally {
			busy = false;
		}
	}

	async function build() {
		busy = true;
		try {
			const res = await rpc.build.build({ runtimeId: id, recipeId: recipe?.recipe?.id ?? '', variant, vars, sandbox, image, force, ref, skipPatches: skipPatches.filter((x) => patches.some((p) => p.id === x)) });
			if (!res.task) throw new Error('the daemon answered without a task');
			followTask(res.task, `Build ${rt?.name}`);
			ontab('installs');
		} catch (err) {
			toast.error('Build failed', reason(err));
		} finally {
			busy = false;
		}
	}

	// Opens the install form on the source build method
	function startBuild() {
		if (buildMethod) method = buildMethod;
		ontab('install');
	}

	async function removeInstall(i: Install) {
		try {
			await rpc.runtimes.removeInstall({ id: i.id });
			toast.success(`Removed ${installLabel(i)}`);
		} catch (err) {
			toast.error('Remove refused', reason(err));
		}
	}

	async function removeBuild(b: Build) {
		try {
			await rpc.build.removeBuild({ id: b.id });
			toast.success(`Removed ${b.variant || b.recipeId}`);
		} catch (err) {
			toast.error('Remove refused', reason(err));
		}
	}

	function users(installId: string) {
		const slots = [...live.slots.values()].filter((s) => s.installId === installId).map((s) => s.name);
		const instances = [...live.instances.values()].filter((i) => i.installId === installId).map((i) => i.name);
		return { slots, instances };
	}

	// How an install came to be, in the words of the method that makes one
	function installWord(i: Install): string {
		return i.kind === InstallKind.ADOPTED ? 'existing' : i.kind === InstallKind.BUILT ? 'built' : 'downloaded';
	}

	function installRows(i: Install) {
		return [
			{ label: 'origin', value: i.origin, mono: true },
			{ label: 'path', value: i.path, mono: true, copy: true },
			{ label: 'directory', value: i.dir, mono: true },
			{ label: 'created', value: when(i.createdAt) },
			...Object.entries(i.facts).map(([k, v]) => ({ label: factLabel(k), value: v, mono: true }))
		];
	}

	function buildTip(b: Build): string {
		return [b.commit ? `commit ${b.commit}` : '', `sandbox ${enumLabel(SandboxKindSchema, b.sandbox)}`, b.image ? `image ${b.image}` : '', b.dir ? `directory ${b.dir}` : '', b.binary ? `binary ${b.binary}` : ''].filter(Boolean).join('\n');
	}

	// The stored defaults for this runtime, merged under every slot and launch
	const stored = $derived(live.settings?.runtimeDefaults[id]?.params ?? {});
	let draft = $state<Record<string, string>>({});
	let draftFrom = '';
	let savingDefaults = $state(false);
	let defaultsError = $state<string | undefined>(undefined);
	const draftDirty = $derived(JSON.stringify(draft) !== JSON.stringify(stored));

	// Takes the stored defaults into the form whenever they change underneath it
	$effect(() => {
		const key = JSON.stringify(stored);
		if (key === draftFrom) return;
		draftFrom = key;
		draft = { ...stored };
	});

	async function saveDefaults() {
		savingDefaults = true;
		defaultsError = undefined;
		try {
			const base = live.settings ?? (await rpc.settings.getSettings({})).settings;
			if (!base) throw new Error('the daemon answered without settings');
			const settings = clone(SettingsSchema, base);
			const params = Object.fromEntries(Object.entries(draft).filter(([, v]) => v.trim() !== ''));
			if (Object.keys(params).length) settings.runtimeDefaults[id] = create(RuntimeDefaultsSchema, { params });
			else delete settings.runtimeDefaults[id];
			await rpc.settings.updateSettings({ settings });
			toast.success(`Saved ${rt?.name ?? id} defaults`);
		} catch (err) {
			defaultsError = reason(err);
		} finally {
			savingDefaults = false;
		}
	}
</script>

{#snippet buildField(f: BuildField)}
	{#if f.kind === 'variant'}
		<Select bind:value={variant} options={variants} label="Variant" />
	{:else if f.kind === 'sandbox'}
		<Segmented bind:value={sandbox} options={[{ value: SandboxKind.HOST, label: 'Host' }, { value: SandboxKind.OCI, label: 'Container' }]} label="Build in" />
	{:else if f.kind === 'image'}
		<Text bind:value={image} label="Container image" mono />
	{:else if f.kind === 'ref'}
		<Text bind:value={ref} label="Branch, tag or commit" max={64} mono />
	{:else if f.kind === 'force'}
		<Switch bind:checked={force} label="Rebuild when already built" />
	{:else if f.kind === 'var'}
		{@const v = f.v}
		{#if v.choices.length}
			<Select value={vars[v.name] || v.default} options={v.choices.map((c) => ({ value: c, label: c }))} label={v.label || v.name} info={v.description || undefined} onchange={(x) => (vars[v.name] = x ?? '')} />
		{:else}
			<Text value={vars[v.name] ?? ''} label={v.label || v.name} info={v.description || undefined} placeholder={v.default || undefined} onchange={(x) => (vars[v.name] = x)} />
		{/if}
	{/if}
{/snippet}

{#snippet installForm()}
	<div class="form" in:reveal>
		<Stack gap={24}>
			<FormSection>
				<Select bind:value={method} options={methods} label="Method" />
				{#if option?.unmet.length}
					<div class="unmet secondary"><Dot lamp="warning" label="unmet" /><Str value={option.unmet.join(', ')} kind="message" color="muted" width="100%" /></div>
				{/if}
			</FormSection>
			{#if option?.method?.kind === InstallKind.ADOPTED}
				<FormSection>
					<Path bind:value={adoptPath} label="Binary" filter="executables" />
				</FormSection>
			{:else if option?.method?.kind === InstallKind.PREBUILT}
				{#if option.fields.length}
					<FormSection>
						<ConfigFields fields={option.fields} bind:values={settings} />
					</FormSection>
				{/if}
			{:else if option?.method?.kind === InstallKind.BUILT}
				<FormSection>
					{#each pairs(buildFields) as pair, n (n)}
						<FieldRow>
							{#each pair as f (f.kind === 'var' ? `var:${f.v.name}` : f.kind)}{@render buildField(f)}{/each}
						</FieldRow>
					{/each}
					{#if missing.length}
						<Str value={missingText(missing)} kind="message" color="error" width="100%" />
					{/if}
				</FormSection>
				{#if patches.length}
					<FormSection title="Patches">
						<List head={{ primary: 'Patch', action: 'Apply' }} label="Patches" compactBelow={0}>
							{#each patches as p (p.id)}
								<ListRow primary={p.id} primaryKind="version">
									{#snippet action()}
										<Switch checked={!skipPatches.includes(p.id)} aria={`Apply ${p.id}`} onchange={(on) => togglePatch(p.id, on)} />
									{/snippet}
									{#snippet below()}<span class="secondary muted">{p.description}</span>{/snippet}
								</ListRow>
							{/each}
						</List>
					</FormSection>
				{/if}
			{/if}
		</Stack>
		<SaveBar always dirty saving={busy} label={verb} disabled={option?.method?.kind === InstallKind.BUILT && (missing.length > 0 || !variant)} onsave={commit} ondiscard={() => ontab('installs')} />
	</div>
{/snippet}

<PanelHeader title={rt?.name ?? id} subtitle={rt?.description || undefined} {onclose} />
<div class="tabrow">
	<PageTabs {tabs} active={shown} onchange={ontab} />
	{#if shown === 'installs' && status.installs.length && !installing}
		<NeoButton elevation={2} onclick={() => ontab('install')}>
			{#snippet icon()}<DownloadSimpleIcon size={16} />{/snippet}
			Install
		</NeoButton>
	{:else if shown === 'builds' && buildMethod}
		<NeoButton elevation={2} onclick={startBuild}>
			{#snippet icon()}<HammerIcon size={16} />{/snippet}
			Build
		</NeoButton>
	{/if}
</div>
<div class="body">
	{#if shown === 'installs'}
		{#if installing}{@render installForm()}{/if}
		{#if !installList.settled}
			<Skeleton rows={2} primary="12ch" secondary="24ch" facts={0} menu={false} />
		{:else if installList.error}
			<Empty text="Installs failed to list" hint={installList.error} action="Retry" onclick={() => installList.refresh()} />
		{:else if installs.length === 0}
			{#if !installing}
				{#if status.installs.length}
					<Empty icon={DownloadSimpleIcon} text="Not installed" action="Install" primary onclick={() => ontab('install')} />
				{:else}
					<Empty icon={DownloadSimpleIcon} text="No install method" />
				{/if}
			{/if}
		{:else}
			<List>
				{#each installs as i (i.id)}
					{@const using = users(i.id)}
					{@const used = using.slots.length + using.instances.length > 0}
					<ListRow lamp={used ? 'success' : 'muted'} shape={used ? 'filled' : 'hollow'} primary={installLabel(i)} primaryKind="version" primaryWidth="12ch" secondary={installWord(i)} secondaryWidth="12ch">
						{#snippet action()}
							<Confirm label="Remove" question="Remove {installLabel(i)}?" danger onconfirm={() => removeInstall(i)} />
						{/snippet}
						{#snippet expand()}
							<KeyRows dense rows={installRows(i)} />
							{#if i.status?.conditions.length}<Conditions conditions={i.status.conditions} />{/if}
							{#if used}
								<KeyRows
									dense
									rows={[
										{ label: 'slots', value: using.slots.join(', '), tip: using.slots.join('\n') },
										{ label: 'instances', value: using.instances.join(', '), tip: using.instances.join('\n') }
									]}
								/>
							{/if}
						{/snippet}
					</ListRow>
				{/each}
			</List>
			<LoadMore next={installList.next} loading={installList.loading} onmore={() => installList.more()} />
		{/if}
	{:else if shown === 'builds'}
		{#if !buildList.settled}
			<Skeleton rows={2} primary="16ch" secondary="12ch" facts={1} />
		{:else if buildList.error}
			<Empty text="Builds failed to list" hint={buildList.error} action="Retry" onclick={() => buildList.refresh()} />
		{:else if builds.length === 0}
			{#if buildMethod}
				<Empty icon={HammerIcon} text="No builds" action="Build" onclick={startBuild} />
			{:else}
				<Empty icon={HammerIcon} text="No builds" />
			{/if}
		{:else}
			<List head={{ primary: 'Variant', secondary: 'Ref', facts: ['Duration'] }}>
				{#each builds as b (b.id)}
					<ListRow lamp={buildLamp(b.state)} primary={b.variant || b.recipeId} primaryTip={buildTip(b)} secondary={b.ref} secondaryKind="version" secondaryWidth="12ch" facts={[{ value: duration(b.createdAt, b.finishedAt, clockTick.now) }]}>
						{#snippet action()}
							<Confirm label="Remove" question="Remove build {b.variant || b.recipeId}?" danger onconfirm={() => removeBuild(b)} />
						{/snippet}
						{#snippet expand()}
							{#if b.error}<p class="error-text failure">{b.error}</p>{/if}
							<KeyRows
								dense
								rows={[
									...Object.entries(b.vars).map(([k, v]) => ({ label: factLabel(k), value: v, mono: true })),
									...Object.entries(b.facts).map(([k, v]) => ({ label: factLabel(k), value: v, mono: true })),
									{ label: 'patches', value: b.patches.join(', '), tip: b.patches.join('\n') },
									{ label: 'task', value: shortId(b.taskId), href: b.taskId ? `/tasks/${b.taskId}` : undefined, mono: true }
								]}
							/>
						{/snippet}
					</ListRow>
				{/each}
			</List>
			<LoadMore next={buildList.next} loading={buildList.loading} onmore={() => buildList.more()} />
		{/if}
	{:else if shown === 'defaults'}
		{#if !rt?.params.length}
			<Empty icon={SlidersHorizontalIcon} text="No parameters" />
		{:else}
			<Stack gap={24}>
				<ParamsForm params={rt.params} bind:values={draft} />
			</Stack>
			<SaveBar dirty={draftDirty} saving={savingDefaults} error={defaultsError} onsave={saveDefaults} ondiscard={() => (draft = { ...stored })} />
		{/if}
	{/if}
</div>

<style>
	.tabrow {
		display: flex;
		gap: var(--nebu-space-3);
		align-items: center;
		justify-content: space-between;
		min-width: 0;
	}
	.body {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-4);
		min-width: 0;
		margin-top: var(--nebu-space-3);
	}
	.form {
		display: flex;
		flex-direction: column;
		gap: var(--nebu-space-4);
		padding-bottom: var(--nebu-space-4);
		border-bottom: 1px solid var(--nebu-border);
	}
	.form:last-child {
		padding-bottom: 0;
		border-bottom: 0;
	}
	.failure {
		overflow-wrap: anywhere;
		white-space: pre-wrap;
	}
	.unmet {
		display: flex;
		gap: var(--nebu-space-2);
		align-items: center;
		min-width: 0;
	}
</style>
