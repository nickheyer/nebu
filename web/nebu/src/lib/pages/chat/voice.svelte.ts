import { audioFormat, listModels, speech, type ModelEntry } from '#lib/api/gateway.js';
import { reason } from '#lib/api/transport.js';
import type { ChatTurn } from '#lib/proto/nebu/v1/chat_pb.js';
import { toast } from '#lib/shell/notify.svelte.js';

export const browserVoice = 'browser';
export const defaultVoice = 'alloy';
export const canRecord = typeof MediaRecorder !== 'undefined' && typeof navigator.mediaDevices?.getUserMedia === 'function';

const prefix = 'nebu.chat.voice.';
const rate = 16_000;

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

export function speaks(m: ModelEntry): boolean {
	return m.ready && m.capabilities.includes('speech');
}

export function transcribes(m: ModelEntry): boolean {
	return m.ready && m.capabilities.includes('transcription');
}

export function kindOf(f: File): 'image' | 'audio' | undefined {
	if (f.type.startsWith('image/')) return 'image';
	if (f.type.startsWith('audio/') || (!f.type && /\.(wav|mp3|m4a|aac|ogg|oga|opus|flac|weba)$/i.test(f.name))) return 'audio';
	return undefined;
}

export function clock(seconds: number): string {
	return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`;
}

export async function toWav(clip: Blob): Promise<Blob> {
	const decoded = await new OfflineAudioContext(1, 1, rate).decodeAudioData(await clip.arrayBuffer());
	const frames = decoded.length;
	const mono = new Float32Array(frames);
	for (let c = 0; c < decoded.numberOfChannels; c++) {
		const ch = decoded.getChannelData(c);
		for (let i = 0; i < frames; i++) mono[i]! += ch[i]! / decoded.numberOfChannels;
	}
	const out = new DataView(new ArrayBuffer(44 + frames * 2));
	const tag = (at: number, s: string) => {
		for (let i = 0; i < s.length; i++) out.setUint8(at + i, s.charCodeAt(i));
	};
	tag(0, 'RIFF');
	out.setUint32(4, 36 + frames * 2, true);
	tag(8, 'WAVE');
	tag(12, 'fmt ');
	out.setUint32(16, 16, true);
	out.setUint16(20, 1, true);
	out.setUint16(22, 1, true);
	out.setUint32(24, rate, true);
	out.setUint32(28, rate * 2, true);
	out.setUint16(32, 2, true);
	out.setUint16(34, 16, true);
	tag(36, 'data');
	out.setUint32(40, frames * 2, true);
	for (let i = 0; i < frames; i++) {
		const s = Math.max(-1, Math.min(1, mono[i]!));
		out.setInt16(44 + i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true);
	}
	return new Blob([out], { type: 'audio/wav' });
}

export async function sendable(f: File): Promise<File> {
	if (audioFormat(f.type)) return f;
	return new File([await toWav(f)], `${f.name.replace(/\.[^.]*$/, '') || 'audio'}.wav`, { type: 'audio/wav' });
}

export function speakable(md: string): string {
	return md
		.replace(/<think>[\s\S]*?(<\/think>|$(?![\s\S]))/gi, ' ')
		.replace(/<(tool_call|tool_response|function_call)>[\s\S]*?(<\/\1>|$(?![\s\S]))/gi, ' ')
		.replace(/<\|[^|>]*\|>/g, ' ')
		.replace(/^[ \t]*(```|~~~)[\s\S]*?(^[ \t]*\1[^\n]*|$(?![\s\S]))/gm, ' ')
		.replace(/\$\$[\s\S]*?\$\$/g, ' ')
		.replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
		.replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
		.replace(/<\/?[a-z][^>]*>/gi, ' ')
		.replace(/`([^`\n]*)`/g, '$1')
		.replace(/^[ \t]*\|?[ \t:|-]*-[ \t:|-]*\|[ \t:|-]*$/gm, '')
		.replace(/^[ \t]*([-*_][ \t]*){3,}$/gm, '')
		.replace(/^[ \t]*(#{1,6}|>+|[-*+])[ \t]+/gm, '')
		.replace(/^[ \t]*\|[ \t]*|[ \t]*\|[ \t]*$/gm, '')
		.replace(/[ \t]*\|[ \t]*/g, ', ')
		.replace(/(\*\*|__|~~|\*)(?=\S)([^\n]*?\S)\1/g, '$2')
		.replace(/[ \t]+/g, ' ')
		.replace(/\n\s*\n+/g, '\n')
		.trim();
}

function sentences(words: string): string[] {
	const out: string[] = [];
	let cur = '';
	for (const s of words.match(/[^.!?\n]+[.!?]*\s*|\n+/g) ?? [words]) {
		if (cur && cur.length + s.length > 200) {
			out.push(cur);
			cur = '';
		}
		cur += s;
	}
	out.push(cur);
	return out.map((s) => s.trim()).filter(Boolean);
}

export class Recorder {
	on = $state(false);
	seconds = $state(0);
	#rec: MediaRecorder | undefined;
	#keep = false;

	async record(limit = 480): Promise<Blob | undefined> {
		this.#keep = true;
		this.seconds = 0;
		this.on = true;
		let timer: ReturnType<typeof setInterval> | undefined;
		let stream: MediaStream | undefined;
		try {
			stream = await navigator.mediaDevices.getUserMedia({ audio: true });
			if (!this.#keep) return undefined;
			const rec = new MediaRecorder(stream);
			const chunks: Blob[] = [];
			this.#rec = rec;
			timer = setInterval(() => {
				if (++this.seconds >= limit) this.stop();
			}, 1000);
			return await new Promise<Blob | undefined>((resolve, reject) => {
				rec.ondataavailable = (e) => {
					if (e.data.size) chunks.push(e.data);
				};
				rec.onstop = () => resolve(this.#keep && chunks.length ? new Blob(chunks, { type: rec.mimeType || chunks[0]?.type }) : undefined);
				rec.onerror = () => reject(new Error('the microphone stopped recording'));
				rec.start();
			});
		} finally {
			clearInterval(timer);
			stream?.getTracks().forEach((t) => t.stop());
			this.#rec = undefined;
			this.on = false;
		}
	}

	stop(): void {
		if (this.#rec?.state === 'recording') this.#rec.stop();
	}

	cancel(): void {
		this.#keep = false;
		this.stop();
	}
}

class Voice {
	source = $state(read('source', ''));
	voice = $state(read('voice', ''));
	browser = $state(read('browser', ''));
	auto = $state(read('auto', false));
	routes = $state<string[]>();
	voices = $state.raw<SpeechSynthesisVoice[]>([]);
	playing = $state('');
	#run = 0;
	#abort: AbortController | undefined;
	#audio: HTMLAudioElement | undefined;
	#clips = new Map<string, { sig: string; url: string }>();

	constructor() {
		$effect.root(() => {
			$effect(() => write('source', this.source));
			$effect(() => write('voice', this.voice));
			$effect(() => write('browser', this.browser));
			$effect(() => write('auto', this.auto));
		});
		if (typeof speechSynthesis !== 'undefined') {
			const load = () => (this.voices = speechSynthesis.getVoices());
			load();
			speechSynthesis.addEventListener('voiceschanged', load);
		}
	}

	get chosen(): string {
		return this.source || this.routes?.[0] || browserVoice;
	}

	get can(): boolean {
		return typeof speechSynthesis !== 'undefined' || this.chosen !== browserVoice;
	}

	toggle(turn: ChatTurn): void {
		if (this.playing === turn.id) this.stop();
		else void this.read(turn);
	}

	async read(turn: ChatTurn): Promise<void> {
		this.stop();
		const run = this.#run;
		try {
			await this.#read(turn, run);
		} catch (err) {
			if (run !== this.#run) return;
			this.stop();
			toast.error('Reading aloud failed', reason(err));
		}
	}

	stop(): void {
		this.#run++;
		this.#abort?.abort();
		this.#abort = undefined;
		this.#audio?.pause();
		this.#audio = undefined;
		if (typeof speechSynthesis !== 'undefined') speechSynthesis.cancel();
		this.playing = '';
	}

	async #read(turn: ChatTurn, run: number): Promise<void> {
		const words = speakable(turn.text);
		if (!words) return;
		this.playing = turn.id;
		if (!this.source && this.routes === undefined) this.routes = (await listModels()).filter(speaks).map((m) => m.id);
		if (run !== this.#run) return;
		const route = this.chosen;
		if (route === browserVoice) return this.#say(words, run);
		const voice = this.voice.trim() || defaultVoice;
		const sig = `${route}\n${voice}\n${words}`;
		const kept = this.#clips.get(turn.id);
		let url = kept?.sig === sig ? kept.url : '';
		if (!url) {
			this.#abort = new AbortController();
			const clip = await speech({ model: route, input: words, voice }, this.#abort.signal);
			if (run !== this.#run) return;
			if (kept) URL.revokeObjectURL(kept.url);
			url = URL.createObjectURL(clip);
			this.#clips.set(turn.id, { sig, url });
		}
		const audio = new Audio(url);
		this.#audio = audio;
		audio.onended = () => {
			if (run === this.#run) this.stop();
		};
		await audio.play();
	}

	#say(words: string, run: number): void {
		if (typeof speechSynthesis === 'undefined') throw new Error('this browser has no voices of its own and no speech route is running');
		const voice = this.voices.find((v) => v.voiceURI === this.browser);
		const parts = sentences(words);
		for (const [i, part] of parts.entries()) {
			const u = new SpeechSynthesisUtterance(part);
			if (voice) {
				u.voice = voice;
				u.lang = voice.lang;
			}
			if (i === parts.length - 1) {
				u.onend = () => {
					if (run === this.#run) this.stop();
				};
			}
			u.onerror = (e) => {
				if (run !== this.#run || e.error === 'interrupted' || e.error === 'canceled') return;
				this.stop();
				toast.error('Reading aloud failed', `the browser's voice answered ${e.error}`);
			};
			speechSynthesis.speak(u);
		}
	}
}

export const voice = new Voice();
