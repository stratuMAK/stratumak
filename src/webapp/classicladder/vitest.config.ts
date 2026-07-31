import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    // .ts only: vue-tsc -b emits gitignored .js next to the sources, which
    // must not be collected as (possibly stale) duplicate suites
    include: ['src/**/*.test.ts'],
    // The store reads window.location.origin at module load to point its
    // client at the server it was served from.
    environment: 'happy-dom',
    environmentOptions: {
      happyDOM: { url: 'http://localhost/' },
    },
  },
});
