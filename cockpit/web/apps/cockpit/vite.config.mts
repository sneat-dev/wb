/// <reference types='vitest' />
import { defineConfig } from 'vite'
import angular from '@analogjs/vite-plugin-angular'

// One unit-test run and one coverage gate for the whole workspace: the
// application and every library under libs/.
export default defineConfig({
  root: import.meta.dirname,
  plugins: [angular()],
  test: {
    name: 'cockpit',
    watch: false,
    globals: true,
    environment: 'jsdom',
    include: ['src/**/*.spec.ts'],
    setupFiles: ['src/test-setup.ts'],
    reporters: ['default'],
  },
})
