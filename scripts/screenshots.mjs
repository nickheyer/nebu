// Drives a headless Chromium over CDP to screenshot pages at the three spec widths and report the fonts in use
import { spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const base = process.env.BASE ?? 'http://localhost:5173';
const out = process.env.OUT ?? 'screens';
const pages = (process.env.PAGES ?? '/,/chat,/models,/discover,/runtimes,/serve,/gateway,/traffic,/mesh,/bots,/tasks,/host,/settings').split(',');
const widths = (process.env.WIDTHS ?? 'standard').split(',');
const user = process.env.NEBU_USER ?? 'nick';
const pass = process.env.NEBU_PASS ?? 'nebu-nebu-1';
const sizes = { mobile: [390, 844], standard: [1440, 900], ultrawide: [1920, 1080] };
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

const { targetId } = await send('Target.createTarget', { url: 'about:blank' });
const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true });
await send('Page.enable', {}, sessionId);
await send('Runtime.enable', {}, sessionId);
const consoleErrors = [];
listeners.push((msg) => {
    if (msg.sessionId !== sessionId) return;
    if (msg.method === 'Runtime.exceptionThrown') consoleErrors.push(msg.params.exceptionDetails.exception?.description ?? msg.params.exceptionDetails.text);
    if (msg.method === 'Runtime.consoleAPICalled' && msg.params.type === 'error') consoleErrors.push(msg.params.args.map((a) => a.value ?? a.description).join(' '));
});

// Waits until the shell or the login page has rendered and the fonts are in
async function settle() {
    await send('Runtime.evaluate', { expression: `new Promise((done) => { const t0 = Date.now(); const tick = () => { const ready = document.querySelector('main') && !document.querySelector('.boot'); if (ready || Date.now() - t0 > 10000) done(); else setTimeout(tick, 100); }; tick(); })`, awaitPromise: true }, sessionId);
    await send('Runtime.evaluate', { expression: 'document.fonts.ready.then(() => new Promise(r => setTimeout(r, 1500)))', awaitPromise: true }, sessionId);
}

// Signs the browser in, creating the first account when the daemon still waits for one
async function signIn() {
    const loaded = once('Page.loadEventFired', sessionId);
    await send('Page.navigate', { url: base + '/login' }, sessionId);
    await loaded;
    await settle();
    const { result } = await send('Runtime.evaluate', { expression: `(async () => {
        const status = await (await fetch('/auth/session', { headers: { Accept: 'application/json' } })).json();
        if (${process.env.LOGOUT ? 'true' : 'false'}) { await fetch('/auth/logout', { method: 'POST' }); return 'signed out'; }
        if (!status.enabled || status.user) return 'signed in';
        const path = status.local && status.local.setup ? '/auth/local/setup' : '/auth/local/login';
        const res = await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: ${JSON.stringify(user)}, password: ${JSON.stringify(pass)} }) });
        return path + ' ' + res.status + (res.ok ? '' : ' refused');
    })()`, awaitPromise: true, returnByValue: true }, sessionId);
    if (String(result.value).endsWith('refused')) throw new Error(`sign in as ${user} ${result.value}, set NEBU_USER and NEBU_PASS`);
    console.log('auth', result.value);
}
await signIn();

const report = [];
for (const width of widths) {
    const [w, h] = sizes[width];
    await send('Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: 1, mobile: width === 'mobile' }, sessionId);
    for (const path of pages) {
        let result;
        // A page that redirects while being read is read again once it lands
        for (let attempt = 0; ; attempt++) {
            try {
                const loaded = once('Page.loadEventFired', sessionId);
                await send('Page.navigate', { url: base + path }, sessionId);
                await loaded;
                consoleErrors.length = 0;
                await settle();
                ({ result } = await send('Runtime.evaluate', { expression: `JSON.stringify({ body: getComputedStyle(document.body).fontFamily, sans: document.fonts.check('15px "IBM Plex Sans"'), mono: document.fonts.check('13px "IBM Plex Mono"'), faces: [...document.fonts].filter(f => f.status === 'loaded').map(f => f.family + ' ' + f.weight), scrollX: document.documentElement.scrollWidth > document.documentElement.clientWidth, title: document.title, errors: (window.__errors ?? []).length })`, returnByValue: true }, sessionId));
                break;
            } catch (err) {
                if (attempt >= 2 || !String(err.message).includes('navigated')) throw err;
                await new Promise((r) => setTimeout(r, 1500));
            }
        }
        const info = JSON.parse(result.value);
        const { data } = await send('Page.captureScreenshot', { format: 'png' }, sessionId);
        const name = `${width}-${path === '/' ? 'overview' : path.replace(/^\//, '').replace(/[\/?=&]/g, '_')}.png`;
        writeFileSync(join(out, name), Buffer.from(data, 'base64'));
        report.push({ width, path, file: name, ...info, consoleErrors: [...consoleErrors] });
        console.log(`${name}\tsans=${info.sans}\tmono=${info.mono}\tscrollX=${info.scrollX}\terrors=${consoleErrors.length}`);
        for (const e of consoleErrors) console.log('   ! ' + String(e).split('\n')[0].slice(0, 200));
    }
}
console.log('faces ' + JSON.stringify([...new Set(report.flatMap((r) => r.faces))]));
ws.close();
chrome.kill();