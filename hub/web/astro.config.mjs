// @ts-check
import { defineConfig } from 'astro/config';

export default defineConfig({
  site: 'https://sneat.work',
  base: '/workbench',
  trailingSlash: 'always',
  build: { format: 'directory' },
});
