import { onSessionExpired } from '#lib/api/transport.js';

export type UserInfo = {
	subject: string;
	email: string;
	name: string;
	provider: string;
	role: string;
	expires: string;
};

export type AuthStatus = {
	enabled: boolean;
	sso: { name: string } | null;
	local: { setup: boolean } | null;
	user: UserInfo | null;
};

export type Role = 'admin' | 'operator' | 'inference';

type Credentials = { username: string; password: string };

// Reads the error field of a refused auth answer
async function refusal(res: Response): Promise<string> {
	const body = (await res.json().catch(() => null)) as { error?: string } | null;
	return body?.error ?? `${res.status} ${res.statusText}`;
}

async function post(path: string, body?: Credentials): Promise<Response> {
	return fetch(path, {
		method: 'POST',
		headers: body ? { 'Content-Type': 'application/json' } : {},
		body: body ? JSON.stringify(body) : undefined
	});
}

export class Session {
	status = $state<AuthStatus | null>(null);
	error = $state<string | null>(null);
	busy = $state(false);

	constructor() {
		onSessionExpired(() => void this.load());
	}

	get ready(): boolean {
		return this.status !== null;
	}

	get signedIn(): boolean {
		return this.status !== null && (!this.status.enabled || this.status.user !== null);
	}

	get user(): UserInfo | null {
		return this.status?.user ?? null;
	}

	// The caller's role, admin when auth is off
	get role(): Role {
		if (!this.status?.enabled) return 'admin';
		const r = this.user?.role;
		return r === 'operator' || r === 'inference' ? r : 'admin';
	}

	get admin(): boolean {
		return this.role === 'admin';
	}

	// Whether the caller may list and read daemon records
	get reads(): boolean {
		return this.role !== 'inference';
	}

	get displayName(): string {
		return this.user?.name || this.user?.email || this.user?.subject || '';
	}

	async load(): Promise<void> {
		let res: Response;
		try {
			res = await fetch('/auth/session', { headers: { Accept: 'application/json' } });
		} catch (err) {
			this.error = err instanceof Error ? err.message : String(err);
			return;
		}
		if (!res.ok) {
			this.error = await refusal(res);
			return;
		}
		this.status = (await res.json()) as AuthStatus;
		this.error = null;
	}

	async login(c: Credentials): Promise<boolean> {
		return this.submit('/auth/local/login', c);
	}

	async setup(c: Credentials): Promise<boolean> {
		return this.submit('/auth/local/setup', c);
	}

	async logout(): Promise<void> {
		this.busy = true;
		try {
			await post('/auth/logout');
			await this.load();
		} finally {
			this.busy = false;
		}
	}

	private async submit(path: string, c: Credentials): Promise<boolean> {
		this.busy = true;
		this.error = null;
		try {
			const res = await post(path, c);
			if (!res.ok) {
				this.error = await refusal(res);
				return false;
			}
			await this.load();
			return true;
		} finally {
			this.busy = false;
		}
	}
}

export const session = new Session();
