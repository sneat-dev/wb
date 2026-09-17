# wb bench dashboard

The Astro source for the bench dashboard — the pages behind `wb`'s
`/workbench/dashboard/`, `/workbench/dashboard/github/`, `/workbench/org/*`,
`/workbench/repo/*`, and `/workbench/app/sync-report/` routes. Marketing pages
for sneat.work/bench live in a separate, private repository.

```
pnpm install && pnpm build
pnpm test
pnpm test:e2e
```

`wb` embeds this `dist/` and serves it at `/workbench/`; the hosted
sneat.dev/wb site serves its own rewritten copy of the same build under `/wb`.
