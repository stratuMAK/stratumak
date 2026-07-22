import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    // .ts only: vue-tsc -b emits gitignored .js next to the sources, which
    // must not be collected as (possibly stale) duplicate suites
    include: ['src/**/*.test.ts'],
    environment: 'happy-dom',
    environmentOptions: {
      happyDOM: { url: 'http://localhost/' },
    },
  },
});
