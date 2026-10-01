import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    projects: [
      'apps/*/vite.config.mts',
      'libs/**/vite.config.mts',
      {
        test: {
          name: 'tools',
          environment: 'node',
          include: ['tools/**/*.spec.mjs', 'apps/*-e2e/src/**/*.spec.ts'],
        },
      },
    ],
    coverage: {
      provider: 'v8',
      reportsDirectory: 'coverage',
      reporter: ['text-summary', 'text'],
      include: ['apps/*/src/**/*.ts', 'libs/**/src/**/*.ts', 'tools/**/*.mjs'],
      // Test files, the application's bootstrap file, the end-to-end test
      // itself (*.e2e.ts, which Playwright runs), its shared support module and the CLI entry
      // wrappers in tools/ (each a thin shell over a tested function in tools/lib; the preview
      // and shots entries only load the fixtures and drive a browser) are the only exclusions.
      exclude: [
        '**/*.spec.ts',
        '**/*.spec.mjs',
        'tools/check-component-specs.mjs',
        'tools/finish-build.mjs',
        'tools/serve-dist.mjs',
        'tools/fixtures.mjs',
        'tools/preview-fixture.mjs',
        'tools/shots.mjs',
        'tools/journey-guard.mjs',
        '**/test-setup.ts',
        '**/*.d.ts',
        'apps/*/src/main.ts',
        'apps/*-e2e/src/**/*.e2e.ts',
        // What the stubbed Playwright tests share: run by them, as they are.
        'apps/*-e2e/src/support.ts',
      ],
      thresholds: {
        statements: 100,
        branches: 100,
        functions: 100,
        lines: 100,
      },
    },
  },
})
