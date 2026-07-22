# WB website

This is the source for [wb.sneat.dev](https://wb.sneat.dev), kept with the WB
CLI because it documents and presents the same product.

## Local development

```sh
cd website
pnpm install --frozen-lockfile
pnpm dev
```

Use `pnpm build` for the production check. `pnpm deploy` is an explicit
Cloudflare Workers deployment; it is intentionally not run by CI.

The site can link to repository context supplied by CodeGrapher, but it does
not publish locally generated WB reports or repository source code.
