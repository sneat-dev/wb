import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    include: ['tests/*.test.ts'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html'],
      // Measured 2026-09-23 with `pnpm exec vitest run --coverage`: statements
      // 20.59%, branches 65.36%, functions 72.41%, lines 20.59%. Each floor is
      // that measurement rounded down to a whole number, never above it. Raise
      // with real tests as coverage grows; never lower to fit.
      thresholds: {
        statements: 20,
        lines: 20,
        functions: 72,
        branches: 65,
      },
    },
  },
});
