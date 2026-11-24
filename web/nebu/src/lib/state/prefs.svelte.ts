const prefix = 'nebu.';

function read<T>(key: string, fallback: T): T {
	try {
		const raw = localStorage.getItem(prefix + key);
		return raw === null ? fallback : (JSON.parse(raw) as T);
	} catch {
		return fallback;
	}
}

function write(key: string, value: unknown): void {
	try {
		localStorage.setItem(prefix + key, JSON.stringify(value));
	} catch {
		return;
	}
}

// Choices the browser remembers between visits
class Prefs {
	railCollapsed = $state(read('rail', false));
	discoverSource = $state(read('discover.source', ''));
	discoverSourceLabel = $state(read('discover.sourceLabel', ''));
	// The columns picked for each source's results, unset while the source's own choice stands
	discoverColumns = $state<Record<string, string[]>>(read('discover.columns', {}));
	// Whether the chat settings stand open beside the thread, unset until the person opens or closes them
	chatSettingsOpen = $state<boolean | null>(read('chat.settings', null));
	chatTheater = $state(read('chat.theater', false));
	trafficWindow = $state(read('traffic.window', '5m'));
	trafficTimeline = $state(read('traffic.timeline', false));
	serveDismissed = $state<string[]>(read('serve.dismissed', []));
	morpheusOpen = $state(read('morpheus.open', false));
	// The route Morpheus talks to, the first ready text route while empty
	morpheusModel = $state(read('morpheus.model', ''));
	// Whether Morpheus waits for the person before a call that changes something
	morpheusAsks = $state(read('morpheus.asks', true));

	constructor() {
		$effect.root(() => {
			$effect(() => write('rail', this.railCollapsed));
			$effect(() => write('discover.source', this.discoverSource));
			$effect(() => write('discover.sourceLabel', this.discoverSourceLabel));
			$effect(() => write('discover.columns', this.discoverColumns));
			$effect(() => write('chat.settings', this.chatSettingsOpen));
			$effect(() => write('chat.theater', this.chatTheater));
			$effect(() => write('traffic.window', this.trafficWindow));
			$effect(() => write('traffic.timeline', this.trafficTimeline));
			$effect(() => write('serve.dismissed', this.serveDismissed));
			$effect(() => write('morpheus.open', this.morpheusOpen));
			$effect(() => write('morpheus.model', this.morpheusModel));
			$effect(() => write('morpheus.asks', this.morpheusAsks));
		});
	}
}

export const prefs = new Prefs();
