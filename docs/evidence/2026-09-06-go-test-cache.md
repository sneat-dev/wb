# Go covered-test cache receipt — 2026-09-06

This receipt records one local experiment proving that an unchanged covered Go
package can reuse the test-result cache when WB does not inject `-count=1`.

- WB source: `d6a23d9ea26f17f1ceb48532b0c89b69cf6d76d2`
- Installed runner: `sneat-dev/wb v0.109.2`
- Go toolchain: `go1.27.1 darwin/arm64`
- Package: `github.com/sneat-dev/wb/internal/quality`
- Test selection: `TestGoCoverageArgumentsKeepTestResultCacheEnabled`
- Cache: a new, previously absent `/private/tmp/wb-value-cache-proof-20260906`

First command:

```sh
wb run -- env GOCACHE=/private/tmp/wb-value-cache-proof-20260906 \
  go test ./internal/quality \
  -run TestGoCoverageArgumentsKeepTestResultCacheEnabled \
  -coverprofile=/private/tmp/wb-value-cache-proof-first.cov
```

Output:

```text
ok  github.com/sneat-dev/wb/internal/quality  2.353s  coverage: 0.5% of statements
```

Second command changed only the coverage-profile output path:

```sh
wb run -- env GOCACHE=/private/tmp/wb-value-cache-proof-20260906 \
  go test ./internal/quality \
  -run TestGoCoverageArgumentsKeepTestResultCacheEnabled \
  -coverprofile=/private/tmp/wb-value-cache-proof-second.cov
```

Output:

```text
ok  github.com/sneat-dev/wb/internal/quality  (cached)  coverage: 0.5% of statements
```

Both generated profiles had SHA-256
`fb13a825140ad14060d8efc0d0283acb77d76d2061a5254ae6db979dbe88757d`.

The `2.353s` value is Go's reported package time, not end-to-end wall time. This
is one package on one local toolchain. It proves cache reuse under these exact
inputs; it does not predict a fleet-wide percentage or remote CI saving.
