import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [tailwindcss(), sveltekit()],
  server: {
    proxy: {
      '/nebu.v1.': { target: process.env.NEBU_ADDR ?? 'http://127.0.0.1:8484', changeOrigin: true },
      '/auth': { target: process.env.NEBU_ADDR ?? 'http://127.0.0.1:8484', changeOrigin: true },
      '/files': { target: process.env.NEBU_ADDR ?? 'http://127.0.0.1:8484', changeOrigin: true },
      '/v1': { target: process.env.NEBU_ADDR ?? 'http://127.0.0.1:8484', changeOrigin: true },
      '/sdcpp': { target: process.env.NEBU_ADDR ?? 'http://127.0.0.1:8484', changeOrigin: true },
      '/health': { target: process.env.NEBU_ADDR ?? 'http://127.0.0.1:8484', changeOrigin: true }
    }
  }
});
