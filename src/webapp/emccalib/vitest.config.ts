import { defineConfig } from 'vitest/config';

export default defineConfig({
  resolve: {
    // `vue-tsc -b` (run by `npm run build`) emits gitignored .js artifacts
    // next to the .ts sources, and Vite's default extension order would
    // resolve extensionless imports to those stale .js files. Prefer .ts so
    // the tests always exercise the sources.
    extensions: ['.ts', '.mts', '.tsx', '.mjs', '.js', '.jsx', '.json'],
  },
  test: {
    environment: 'happy-dom',
    // Only .ts test sources — never the .js artifacts vue-tsc emits for them.
    include: ['src/**/*.test.ts'],
  },
});
