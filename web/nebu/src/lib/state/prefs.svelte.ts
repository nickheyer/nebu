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
	chatSettingsOpen = $state(read('chat.settings', true));
	trafficWindow = $state(read('traffic.window', '5m'));
	trafficTimeline = $state(read('traffic.timeline', false));
	serveDismissed = $state<string[]>(read('serve.dismissed', []));

	constructor() {
		$effect.root(() => {
			$effect(() => write('rail', this.railCollapsed));
			$effect(() => write('discover.source', this.discoverSource));
			$effect(() => write('chat.settings', this.chatSettingsOpen));
			$effect(() => write('traffic.window', this.trafficWindow));
			$effect(() => write('traffic.timeline', this.trafficTimeline));
			$effect(() => write('serve.dismissed', this.serveDismissed));
		});
	}
}

export const prefs = new Prefs();
