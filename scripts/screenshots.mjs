// Drives a headless Chromium over CDP to screenshot every scene at the three spec widths in both themes and report the fonts in use
import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { join } from 'node:path';

const base = process.env.BASE ?? 'http://localhost:5173';
const out = process.env.OUT ?? 'screens';
const widths = (process.env.WIDTHS ?? 'mobile,standard,ultrawide').split(',');
const themes = (process.env.THEMES ?? 'light,dark').split(',');
const user = process.env.NEBU_USER ?? '';
const pass = process.env.NEBU_PASS ?? '';
const prefix = 'screenshots-';
const sizes = { mobile: [390, 844], standard: [1440, 900], ultrawide: [1920, 1080], wqhd: [2560, 1440] };

// Every page and its open panel, drawer, dialog or wizard, reached by route, by pressing the named control, or by clicking the first element a selector finds
const allScenes = [
    { id: 'overview', path: '/' },
    { id: 'chat', path: '/chat' },
    { id: 'chat-settings', path: '/chat', press: 'Settings', widths: ['mobile', 'standard'] },
    { id: 'chat-sampling', path: '/chat', press: 'Sampling', widths: ['ultrawide', 'wqhd'] },
    { id: 'chat-conversations', path: '/chat', press: 'Conversations', widths: ['mobile', 'standard'] },
    { id: 'models', path: '/models' },
    { id: 'models-collect', path: '/models', press: 'Collect' },
    { id: 'models-missing', path: '/models/huggingface/moonshotai%2FKimi-K3.1-Pro/default' },
    { id: 'discover', path: '/discover' },
    { id: 'discover-filters', path: '/discover', press: 'Filters' },
    { id: 'discover-repository', path: '/discover', click: 'main .listrow.clickable' },
    { id: 'discover-repository-files', path: '/discover/huggingface/moonshotai%2FKimi-K3.1-Pro?tab=files' },
    { id: 'discover-repository-fit', path: '/discover/huggingface/moonshotai%2FKimi-K3.1-Pro?tab=fit' },
    { id: 'discover-repository-pull', path: '/discover/huggingface/moonshotai%2FKimi-K3.1-Pro?tab=pull' },
    { id: 'runtimes', path: '/runtimes' },
    { id: 'runtimes-panel', path: '/runtimes/llamacpp' },
    { id: 'runtimes-install', path: '/runtimes/llamacpp/install' },
    { id: 'runtimes-builds', path: '/runtimes/llamacpp/builds' },
    { id: 'runtimes-params', path: '/runtimes/llamacpp/params' },
    { id: 'serve', path: '/serve' },
    { id: 'serve-launch', path: '/serve/launch' },
    { id: 'serve-slot', path: '/serve', press: 'New slot' },
    { id: 'serve-slot-edit', path: '/serve', click: 'main .listrow.clickable' },
    { id: 'gateway', path: '/gateway' },
    { id: 'gateway-route', path: '/gateway/routes/new' },
    { id: 'gateway-info', path: '/gateway/routes', click: 'header [aria-label="Info"]', widths: ['standard', 'ultrawide'] },
    { id: 'gateway-route-open', path: '/gateway/routes', click: 'main .listrow.clickable' },
    { id: 'gateway-pools', path: '/gateway/pools' },
    { id: 'gateway-pool', path: '/gateway/pools/new' },
    { id: 'gateway-pool-open', path: '/gateway/pools', click: 'main .listrow.clickable' },
    { id: 'gateway-objectives', path: '/gateway/objectives' },
    { id: 'gateway-objective', path: '/gateway/objectives/new' },
    { id: 'gateway-schedulers', path: '/gateway/schedulers' },
    { id: 'gateway-scheduler', path: '/gateway/schedulers', click: 'main .listrow.clickable' },
    { id: 'gateway-imports', path: '/gateway/imports' },
    { id: 'traffic', path: '/traffic' },
    { id: 'traffic-timeline', path: '/traffic', press: 'Timeline', widths: ['standard', 'ultrawide', 'wqhd'] },
    { id: 'traffic-filters', path: '/traffic', press: 'Filters' },
    { id: 'traffic-trace', path: '/traffic', click: 'main .listrow.clickable' },
    { id: 'mesh', path: '/mesh' },
    { id: 'mesh-create', path: '/mesh', press: 'Create a mesh' },
    { id: 'mesh-join', path: '/mesh', press: 'Join with a token' },
    { id: 'bots', path: '/bots' },
    { id: 'bots-new', path: '/bots', press: 'New bot' },
    { id: 'bots-page', path: '/bots', click: 'main .listrow.clickable' },
    { id: 'tasks', path: '/tasks' },
    { id: 'tasks-panel', path: '/tasks', click: 'main .listrow.clickable' },
    { id: 'host', path: '/host' },
    { id: 'host-logs', path: '/host/logs' },
    { id: 'settings', path: '/settings' },
    { id: 'settings-sources', path: '/settings/sources' },
    { id: 'settings-source', path: '/settings/sources', click: 'main .listrow.clickable' },
    { id: 'settings-tools', path: '/settings/tools' },
    { id: 'settings-accounts', path: '/settings/accounts' },
    { id: 'settings-tokens', path: '/settings/tokens' },
    { id: 'settings-source-new', path: '/settings/sources', press: 'Add source' },
    { id: 'settings-tool-new', path: '/settings/tools', press: 'Add tool server' },
    { id: 'settings-account-new', path: '/settings/accounts', press: 'Add account' },
    { id: 'settings-token-new', path: '/settings/tokens', press: 'New token' },
    { id: 'settings-account-password', path: '/settings/accounts', press: 'More', click: '[role=menuitem][aria-posinset="1"] button' },
    { id: 'settings-password', path: '/settings/general', press: 'Account', click: '[role=menuitem][aria-posinset="4"] button' },
    { id: 'login', path: '/login', signedOut: true }
];
const chosen = process.env.SCENES ? process.env.SCENES.split(',') : allScenes.map((s) => s.id);
const scenes = allScenes.filter((s) => chosen.includes(s.id));
for (const id of chosen) if (!allScenes.some((s) => s.id === id)) throw new Error(`no scene ${id}, scenes are ${allScenes.map((s) => s.id).join(', ')}`);

mkdirSync(out, { recursive: true });
const port = 9333 + Math.floor(Math.random() * 500);
const profile = mkdtempSync(join(tmpdir(), 'nebu-shot-'));
const chrome = spawn('/usr/bin/chromium', [`--remote-debugging-port=${port}`, '--headless=new', '--no-sandbox', '--disable-gpu', '--hide-scrollbars', `--user-data-dir=${profile}`, '--no-first-run', 'about:blank'], { stdio: 'ignore' });
process.on('exit', () => chrome.kill());

// Waits until the dev server answers and proxies the daemon session endpoint
async function ready() {
    for (let i = 0; i < 3000; i++) {
        const res = await fetch(base + '/auth/session', { headers: { Accept: 'application/json' } }).catch(() => null);
        if (res?.ok) return;
        await new Promise((r) => setTimeout(r, 200));
    }
    throw new Error(`${base} did not come up`);
}

async function wsUrl() {
    for (let i = 0; i < 50; i++) {
        try {
            const res = await fetch(`http://127.0.0.1:${port}/json/version`);
            return (await res.json()).webSocketDebuggerUrl;
        } catch {
            await new Promise((r) => setTimeout(r, 200));
        }
    }
    throw new Error('chromium did not open its debugging port');
}

// The daemon token from NEBU_AUTH_TOKEN or the api.token file in the data dir
function daemonToken() {
    if (process.env.NEBU_AUTH_TOKEN) return process.env.NEBU_AUTH_TOKEN;
    const windows = process.platform === 'win32' && process.env.LOCALAPPDATA;
    const home = windows ? process.env.LOCALAPPDATA : process.platform === 'darwin' ? join(homedir(), 'Library', 'Application Support') : process.env.XDG_DATA_HOME || join(homedir(), '.local', 'share');
    const file = join(process.env.NEBU_DATA_DIR ?? join(home, 'nebu'), 'api.token');
    try {
        return readFileSync(file, 'utf8').trim();
    } catch {
        throw new Error(`no daemon token at ${file}, set NEBU_AUTH_TOKEN or NEBU_DATA_DIR, or sign in as an account with NEBU_USER and NEBU_PASS`);
    }
}

// Calls one AuthService procedure as the daemon over the Connect JSON protocol
async function rpc(token, procedure, body) {
    const res = await fetch(`${base}/nebu.v1.AuthService/${procedure}`, { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` }, body: JSON.stringify(body) });
    const reply = await res.json().catch(() => ({}));
    if (!res.ok) throw new Error(`${procedure} ${res.status}: ${reply.message ?? reply.error ?? res.statusText}`);
    return reply;
}

// Usernames of accounts runs older than ten minutes left behind, younger ones belonging to runs still going
async function leftovers(token) {
    const names = [];
    const before = Date.now() - 10 * 60_000;
    for (let cursor = ''; ; ) {
        const { users = [], nextCursor = '' } = await rpc(token, 'ListUsers', { cursor, limit: 1000 });
        names.push(...users.filter((u) => u.username.startsWith(prefix) && Date.parse(u.createdAt) < before).map((u) => u.username));
        if (!nextCursor) return names;
        cursor = nextCursor;
    }
}

let token = '';
let account = '';

// Removes the account this run made
async function removeAccount() {
    if (!account) return;
    const name = account;
    account = '';
    await rpc(token, 'DeleteUser', { username: name });
    console.log(`auth removed ${name}`);
}

// The account and sign-in path for the run, an admin made for it unless NEBU_USER and NEBU_PASS name one
async function credentials(setup) {
    if (user && pass) return { path: setup ? '/auth/local/setup' : '/auth/local/login', username: user, password: pass };
    if (user || pass) throw new Error('set both NEBU_USER and NEBU_PASS');
    if (setup) throw new Error(`no account exists yet, create the first one at ${base}/login, or set NEBU_USER and NEBU_PASS for the script to create it`);
    token = daemonToken();
    for (const name of await leftovers(token)) await rpc(token, 'DeleteUser', { username: name });
    const username = prefix + randomBytes(4).toString('hex');
    const password = randomBytes(18).toString('base64url');
    await rpc(token, 'CreateUser', { username, password, role: 'ROLE_ADMIN' });
    account = username;
    return { path: '/auth/local/login', username, password };
}

await ready();
const ws = new WebSocket(await wsUrl());
await new Promise((r) => (ws.onopen = r));
let seq = 0;
const waiting = new Map();
const listeners = [];
ws.onmessage = (m) => {
    const msg = JSON.parse(m.data);
    if (msg.id && waiting.has(msg.id)) {
        const { resolve, reject } = waiting.get(msg.id);
        waiting.delete(msg.id);
        if (msg.error) reject(new Error(msg.error.message));
        else resolve(msg.result);
    } else if (msg.method) listeners.forEach((fn) => fn(msg));
};
function send(method, params = {}, sessionId) {
    const id = ++seq;
    ws.send(JSON.stringify({ id, method, params, sessionId }));
    return new Promise((resolve, reject) => waiting.set(id, { resolve, reject }));
}
function once(method, sessionId) {
    return new Promise((resolve) => listeners.push((msg) => msg.method === method && msg.sessionId === sessionId && resolve(msg.params)));
}

const consoleErrors = [];

// A page in the given browser context, attached with its console errors collected
async function openPage(browserContextId) {
    const { targetId } = await send('Target.createTarget', { url: 'about:blank', browserContextId });
    const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true });
    await send('Page.enable', {}, sessionId);
    await send('Runtime.enable', {}, sessionId);
    listeners.push((msg) => {
        if (msg.sessionId !== sessionId) return;
        if (msg.method === 'Runtime.exceptionThrown') consoleErrors.push(msg.params.exceptionDetails.exception?.description ?? msg.params.exceptionDetails.text);
        if (msg.method === 'Runtime.consoleAPICalled' && msg.params.type === 'error') consoleErrors.push(msg.params.args.map((a) => a.value ?? a.description).join(' '));
    });
    return sessionId;
}

// Waits for the shell or login page, the last skeleton, the fonts and the transitions
async function settle(sessionId) {
    await send('Runtime.evaluate', { expression: `new Promise((done) => { const t0 = Date.now(); let quiet = 0; const tick = () => { const ready = document.querySelector('main') && !document.querySelector('.boot') && !document.querySelector('[aria-busy="true"]'); quiet = ready ? quiet + 1 : 0; if (quiet >= 3 || Date.now() - t0 > 10000) done(); else setTimeout(tick, 150); }; tick(); })`, awaitPromise: true }, sessionId);
    await send('Runtime.evaluate', { expression: 'document.fonts.ready.then(() => new Promise(r => setTimeout(r, 800)))', awaitPromise: true }, sessionId);
}

async function navigate(sessionId, path) {
    // A page that redirects while being read is read again once it lands
    for (let attempt = 0; ; attempt++) {
        try {
            const loaded = once('Page.loadEventFired', sessionId);
            await send('Page.navigate', { url: base + path }, sessionId);
            await loaded;
            consoleErrors.length = 0;
            await settle(sessionId);
            return;
        } catch (err) {
            if (attempt >= 2 || !String(err.message).includes('navigated')) throw err;
            await new Promise((r) => setTimeout(r, 1500));
        }
    }
}

// Clicks the first visible element the selector finds, answering whether one was there
async function clickFirst(sessionId, selector) {
    const { result } = await send('Runtime.evaluate', { expression: `(() => { const hit = [...document.querySelectorAll(${JSON.stringify(selector)})].find((el) => el.getClientRects().length > 0); if (!hit) return false; hit.click(); return true; })()`, returnByValue: true }, sessionId);
    if (!result.value) return false;
    await new Promise((r) => setTimeout(r, 400));
    await settle(sessionId);
    return true;
}

// Presses the first visible control reading the word, page body before shell, answering whether one was there
async function press(sessionId, text) {
    const { result } = await send('Runtime.evaluate', { expression: `(() => { const want = ${JSON.stringify(text)}; const reads = (el) => el.getClientRects().length > 0 && ((el.textContent ?? '').trim() === want || el.getAttribute('aria-label') === want); const inMain = [...document.querySelectorAll('main button, main a, main [role=button]')]; const all = [...document.querySelectorAll('button, a, [role=button]')]; const hit = inMain.find(reads) ?? all.find(reads); if (!hit) return false; hit.click(); return true; })()`, returnByValue: true }, sessionId);
    if (!result.value) {
        const { result: seen } = await send('Runtime.evaluate', { expression: `[...document.querySelectorAll('button, a, [role=button]')].filter((el) => el.getClientRects().length > 0).map((el) => (el.textContent ?? '').trim() || el.getAttribute('aria-label') || '').filter(Boolean).join(', ')`, returnByValue: true }, sessionId);
        console.log(`   controls on the page: ${seen.value}`);
        return false;
    }
    await new Promise((r) => setTimeout(r, 400));
    await settle(sessionId);
    return true;
}

// The fonts in use, the theme, and the elements reaching past the right edge, which the spec forbids
async function fonts(sessionId) {
    const { result } = await send('Runtime.evaluate', { expression: `JSON.stringify({ body: getComputedStyle(document.body).fontFamily, sans: document.fonts.check('15px "IBM Plex Sans"'), mono: document.fonts.check('13px "IBM Plex Mono"'), faces: [...document.fonts].filter(f => f.status === 'loaded').map(f => f.family + ' ' + f.weight), scrollX: document.documentElement.scrollWidth > document.documentElement.clientWidth, overflow: [...document.querySelectorAll('body *')].filter((el) => el.getBoundingClientRect().right > document.documentElement.clientWidth + 1 && getComputedStyle(el).position !== 'fixed').slice(0, 4).map((el) => el.tagName.toLowerCase() + (typeof el.className === 'string' && el.className ? '.' + el.className.trim().split(/\\s+/).join('.') : '') + ' right=' + Math.round(el.getBoundingClientRect().right)), title: document.title, theme: document.documentElement.getAttribute('neo-theme') })`, returnByValue: true }, sessionId);
    return JSON.parse(result.value);
}

// Signs the browser in, answering whether auth is on
async function signIn(sessionId) {
    await navigate(sessionId, '/login');
    const status = await (await fetch(base + '/auth/session', { headers: { Accept: 'application/json' } })).json();
    if (!status.enabled) {
        console.log('auth off');
        return false;
    }
    if (!status.local) throw new Error('local accounts are off, the script signs in with a local account alone');
    const { path, username, password } = await credentials(status.local.setup);
    const { result } = await send('Runtime.evaluate', { expression: `fetch(${JSON.stringify(path)}, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: ${JSON.stringify(JSON.stringify({ username, password }))} }).then(async (res) => res.ok ? '' : res.status + ' ' + ((await res.json().catch(() => ({}))).error ?? ''))`, awaitPromise: true, returnByValue: true }, sessionId);
    if (result.value) throw new Error(`sign in as ${username} at ${path}: ${result.value}`);
    console.log(`auth ${path} as ${username}`);
    return true;
}

async function shoot(sessionId, name) {
    const { data } = await send('Page.captureScreenshot', { format: 'png' }, sessionId);
    writeFileSync(join(out, name), Buffer.from(data, 'base64'));
}

async function main() {
    const signedIn = await openPage();
    const authOn = await signIn(signedIn);
    // The login page is read from a context holding no session, so it shows the card
    const { browserContextId } = await send('Target.createBrowserContext');
    const signedOut = await openPage(browserContextId);
    const report = [];
    for (const theme of themes) {
        for (const sessionId of [signedIn, signedOut]) {
            await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: theme }] }, sessionId);
            // A theme a run remembered would outrank the emulated scheme, so the browser forgets it
            await send('Runtime.evaluate', { expression: `Object.keys(localStorage).filter((k) => k.startsWith('neo-')).forEach((k) => localStorage.removeItem(k))` }, sessionId);
        }
        for (const width of widths) {
            const [w, h] = sizes[width];
            for (const sessionId of [signedIn, signedOut]) {
                await send('Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: 1, mobile: width === 'mobile' }, sessionId);
            }
            for (const scene of scenes) {
                if (scene.widths && !scene.widths.includes(width)) continue;
                const name = `${theme === 'dark' ? 'dark-' : ''}${width}-${scene.id}.png`;
                if (scene.signedOut && !authOn) {
                    console.log(`${name}\tno login page while auth is off`);
                    continue;
                }
                const sessionId = scene.signedOut ? signedOut : signedIn;
                // Views an earlier scene switched, such as the traffic timeline, start from their defaults
                await send('Runtime.evaluate', { expression: `Object.keys(localStorage).filter((k) => k.startsWith('nebu.')).forEach((k) => localStorage.removeItem(k))` }, sessionId);
                await navigate(sessionId, scene.path);
                if (scene.press && !(await press(sessionId, scene.press))) {
                    console.log(`${name}\tnothing reads ${scene.press} on ${scene.path}`);
                    continue;
                }
                if (scene.click && !(await clickFirst(sessionId, scene.click))) {
                    console.log(`${name}\tnothing matches ${scene.click} on ${scene.path}`);
                    continue;
                }
                const info = await fonts(sessionId);
                if (process.env.PROBE) console.log('   probe: ' + (await send('Runtime.evaluate', { expression: process.env.PROBE, returnByValue: true }, sessionId)).result.value);
                await shoot(sessionId, name);
                report.push({ theme, width, scene: scene.id, file: name, ...info, consoleErrors: [...consoleErrors] });
                console.log(`${name}\tsans=${info.sans}\tmono=${info.mono}\tscrollX=${info.scrollX}\ttheme=${info.theme}\terrors=${consoleErrors.length}`);
                for (const o of info.overflow) console.log('   > past the right edge: ' + o);
                for (const e of consoleErrors) console.log('   ! ' + String(e).split('\n')[0].slice(0, 200));
            }
        }
    }
    console.log('faces ' + JSON.stringify([...new Set(report.flatMap((r) => r.faces))]));
}

// The first signal removes the account and exits, repeats wait on the same removal
let stopping;
for (const signal of ['SIGINT', 'SIGTERM']) process.on(signal, () => (stopping ??= removeAccount().catch((err) => console.error(err)).finally(() => process.exit(1))));
try {
    await main();
} catch (err) {
    process.exitCode = 1;
    console.error(err);
}
await removeAccount();
ws.close();
chrome.kill();
