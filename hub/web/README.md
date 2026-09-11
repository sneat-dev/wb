# wb bench dashboard

The Astro source for the bench dashboard — the pages behind `wb`'s
`/bench/dashboard/`, `/bench/dashboard/github/`, `/bench/org/*`,
`/bench/repo/*`, and `/bench/app/sync-report/` routes. Marketing pages for
sneat.work/bench live in a separate, private repository.

```
pnpm install && pnpm build
pnpm test
pnpm test:e2e
```

The `dist/` output is mounted at `/bench` by the hosted sneat.work site
today, and will be embedded and served locally by `wb serve` later.
