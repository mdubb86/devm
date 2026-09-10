import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';

export default defineConfig({
  plugins: [svelte()],
  // Vitest runs test files through Vite's SSR pipeline by default, which
  // resolves the "svelte" package to its server-rendering entry point
  // (no `mount`). Forcing the "browser" condition makes it resolve the
  // client build instead, matching how the esbuild bundle runs in the app.
  resolve: { conditions: ['browser'] },
  test: {
    environment: 'happy-dom',
    globals: true,
  },
});
