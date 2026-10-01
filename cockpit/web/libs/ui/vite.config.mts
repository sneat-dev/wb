/// <reference types='vitest' />
import { defineConfig } from 'vite'
import angular from '@analogjs/vite-plugin-angular'
import { workspaceAliases } from '../../vite.aliases.mts'

// This library's unit tests; the workspace root runs them with the rest and
// holds the one coverage gate.
export default defineConfig({
  root: import.meta.dirname,
  plugins: [angular()],
  resolve: { alias: workspaceAliases },
  test: {
    name: 'ui',
    watch: false,
    globals: true,
    environment: 'jsdom',
    include: ['src/**/*.spec.ts'],
    setupFiles: ['src/test-setup.ts'],
    reporters: ['default'],
  },
})
