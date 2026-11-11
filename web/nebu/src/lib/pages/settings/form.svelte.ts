import { clone, create, toJsonString, type DescMessage, type MessageShape } from '@bufbuild/protobuf';
import { untrack } from 'svelte';
import { reason, rpc } from '#lib/api/transport.js';
import { live } from '#lib/live/events.svelte.js';
import { SettingsSchema, type Settings } from '#lib/proto/nebu/v1/settings_pb.js';
import { toast } from '#lib/shell/notify.svelte.js';

// The stored settings and what the config file gives the ones left unset
export class SettingsForm {
	defaults = $state<Settings | undefined>(undefined);
	#asked = $state<Settings | undefined>(undefined);
	error = $state('');

	// Asks again whenever the settings change, a config reload changing the defaults under them
	constructor() {
		$effect(() => {
			void live.settings;
			untrack(() => void this.load());
		});
	}

	get stored(): Settings | undefined {
		return live.settings ?? this.#asked;
	}

	get ready(): boolean {
		return this.stored !== undefined && this.defaults !== undefined;
	}

	async load(): Promise<void> {
		try {
			const res = await rpc.settings.getSettings({});
			this.#asked = res.settings;
			this.defaults = res.defaults;
			this.error = '';
		} catch (err) {
			this.error = reason(err);
		}
	}

	// Stores the settings with one change made to them
	async save(change: (s: Settings) => void, saved: string): Promise<void> {
		const base = this.stored ?? (await rpc.settings.getSettings({})).settings;
		if (!base) throw new Error('the daemon answered without settings');
		const settings = clone(SettingsSchema, base);
		change(settings);
		await rpc.settings.updateSettings({ settings });
		toast.success(saved);
	}
}

// One message of the settings being edited, starting from the stored one or else the config file's
export class SectionDraft<D extends DescMessage> {
	draft = $state() as MessageShape<D>;
	saving = $state(false);
	error = $state<string | undefined>(undefined);
	#form: SettingsForm;
	#schema: D;
	#pick: (s: Settings) => MessageShape<D> | undefined;
	#put: (s: Settings, v: MessageShape<D>) => void;
	#saved: string;

	constructor(form: SettingsForm, schema: D, pick: (s: Settings) => MessageShape<D> | undefined, put: (s: Settings, v: MessageShape<D>) => void, saved: string) {
		this.#form = form;
		this.#schema = schema;
		this.#pick = pick;
		this.#put = put;
		this.#saved = saved;
		this.draft = create(schema);
		// Takes what is in force into the form whenever it changes underneath
		$effect(() => {
			void this.#key;
			untrack(() => this.discard());
		});
	}

	// What is in force, the stored message or else the config file's
	get #source(): MessageShape<D> {
		const stored = this.#form.stored ? this.#pick(this.#form.stored) : undefined;
		const fallback = this.#form.defaults ? this.#pick(this.#form.defaults) : undefined;
		return stored ?? fallback ?? create(this.#schema);
	}

	get #key(): string {
		return toJsonString(this.#schema, this.#source);
	}

	get dirty(): boolean {
		return toJsonString(this.#schema, this.draft) !== this.#key;
	}

	discard(): void {
		this.draft = clone(this.#schema, this.#source);
		this.error = undefined;
	}

	async save(): Promise<void> {
		this.saving = true;
		this.error = undefined;
		try {
			const next = clone(this.#schema, this.draft);
			await this.#form.save((s) => this.#put(s, next), this.#saved);
		} catch (err) {
			this.error = reason(err);
		} finally {
			this.saving = false;
		}
	}
}
