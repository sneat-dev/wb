// @ts-check
import { defineConfig } from 'astro/config';

export default defineConfig({
  site: 'https://sneat.work',
  base: '/bench',
  trailingSlash: 'always',
  build: { format: 'directory' },
});
