import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vite';
import process from 'node:process';

const daemon = process.env.NEBU_DEV_API ?? 'http://127.0.0.1:8484';
const proxied = ['/nebu.v1.', '/grpc.reflection.', '/auth', '/files', '/v1', '/api', '/mcp', '/tokenize', '/detokenize', '/health', '/livez', '/readyz', '/metrics', '/openapi.yaml'];

export default defineConfig({
	plugins: [
		sveltekit({
			preprocess: vitePreprocess(),
			compilerOptions: {
				runes: ({ filename }) => (filename.split(/[/\\]/).includes('node_modules') ? undefined : true)
			},
			adapter: adapter({ pages: 'dist', assets: 'dist', fallback: 'index.html' })
		})
	],
	server: { proxy: Object.fromEntries(proxied.map((p) => [p, { target: daemon, ws: true }])) }
});
