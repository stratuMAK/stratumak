import { defineConfig } from 'vite';
import vue from '@vitejs/plugin-vue';
import { resolve } from 'path';

export default defineConfig({
  plugins: [vue()],
  base: '/app/latency/',
  resolve: {
    alias: { '@': resolve(__dirname, 'src') },
  },
  server: {
    // dev-only proxy to a running stmakd
    proxy: { '/api': 'http://localhost:5080' },
  },
});
