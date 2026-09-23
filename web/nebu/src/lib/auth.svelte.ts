import { baseUrl } from './api';

export interface AuthUser {
  subject: string;
  email: string;
  name: string;
  provider: 'local' | 'oidc';
  expires: string;
}

// How the daemon authenticates browsers, and who is signed in.
export const auth = $state({
  enabled: true,
  // Single sign-on provider name, empty when off.
  sso: '',
  // Local accounts, null when off. Setup means no account exists yet.
  local: null as { setup: boolean } | null,
  user: null as AuthUser | null
});

// Whether a browser can sign in at all, through an account or single sign-on.
export function signInOffered(): boolean {
  return !!auth.local || !!auth.sso;
}

// Asks the daemon how it authenticates and who the current session is.
export async function refreshAuth(): Promise<void> {
  const resp = await fetch(baseUrl + '/auth/session', { cache: 'no-store' });
  if (!resp.ok) throw new Error(`${resp.status}: ${await resp.text()}`);
  const st = (await resp.json()) as { enabled: boolean; sso: { name: string } | null; local: { setup: boolean } | null; user: AuthUser | null };
  auth.enabled = st.enabled;
  auth.sso = st.sso?.name ?? '';
  auth.local = st.local;
  auth.user = st.user;
}

// Where the browser goes for single sign-on, returning to next afterwards. Needs a full page load.
export function ssoLoginUrl(next: string): string {
  return `${baseUrl}/auth/oidc/login?next=${encodeURIComponent(next)}`;
}

async function credentials(path: string, username: string, password: string): Promise<void> {
  const resp = await fetch(baseUrl + path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username, password }) });
  const text = await resp.text();
  if (!resp.ok) throw new Error(errorText(text, resp.status));
  const st = JSON.parse(text) as { user: AuthUser };
  auth.user = st.user;
  if (auth.local) auth.local.setup = false;
}

// The daemon answers sign-in failures as JSON with an error field.
function errorText(text: string, status: number): string {
  try {
    const parsed = JSON.parse(text) as { error?: string };
    if (parsed.error) return parsed.error;
  } catch {
    // Not JSON, use the body as sent.
  }
  return text.trim() || `${status}`;
}

export const login = (username: string, password: string) => credentials('/auth/local/login', username, password);
export const setup = (username: string, password: string) => credentials('/auth/local/setup', username, password);

export async function logout(): Promise<void> {
  const resp = await fetch(baseUrl + '/auth/logout', { method: 'POST' });
  if (!resp.ok) throw new Error(`${resp.status}: ${await resp.text()}`);
  auth.user = null;
}

// Gateway requests carry the session cookie once signed in. Otherwise they stay same-origin,
// so gateway listeners on other hosts keep answering without credential headers.
export function gatewayCredentials(): RequestCredentials {
  return auth.user ? 'include' : 'same-origin';
}
