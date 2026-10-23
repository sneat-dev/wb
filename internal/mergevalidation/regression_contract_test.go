package mergevalidation

import (
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestWorktreeMergeValidationRegressionMatchesOnlyEquivalentBaselineFailures(t *testing.T) {
	t.Parallel()
	failing := func(detail string) quality.VerificationReport {
		return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
			Language: "go", Module: ".", Check: quality.CheckTest, Command: "go test ./...", Status: quality.StatusFailed, Detail: detail,
		}}}
	}
	specFailing := func(detail string) quality.VerificationReport {
		return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
			Language: "specscore", Module: ".", Check: quality.CheckSpec, Command: "specscore spec lint", Status: quality.StatusFailed, Detail: detail,
		}}}
	}
	nodeFailing := func(detail string) quality.VerificationReport {
		return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
			Language: "node", Module: "frontend", Check: quality.CheckBuild, Command: "pnpm run build", Status: quality.StatusFailed, Detail: detail,
		}}}
	}
	for _, test := range []struct {
		name      string
		baseline  quality.VerificationReport
		candidate quality.VerificationReport
		wantError bool
	}{
		{name: "passing target and candidate", baseline: quality.VerificationReport{Status: quality.StatusPassed}, candidate: quality.VerificationReport{Status: quality.StatusPassed}},
		{name: "same failure at different snapshot paths", baseline: failing("/tmp/target/app.go:3: undefined: missing"), candidate: failing("/tmp/candidate/app.go:3: undefined: missing")},
		{name: "coverage failure subset with changed shard placement", baseline: failing("WB coverage failure index:\n- [github.com/sneat-dev/wb/internal/worktrees shard 2/8] TestStable\n- [unsharded packages] TestRemoved\nWB coverage raw output\nbaseline output"), candidate: failing("WB coverage failure index:\n- [github.com/sneat-dev/wb/internal/worktrees shard 6/8] TestStable\nWB coverage raw output\ncandidate output")},
		{name: "coverage failure introduces test", baseline: failing("WB coverage failure index:\n- [github.com/sneat-dev/wb/internal/worktrees shard 2/8] TestStable\nWB coverage raw output\nbaseline output"), candidate: failing("WB coverage failure index:\n- [github.com/sneat-dev/wb/internal/worktrees shard 6/8] TestStable\n- [unsharded packages] TestNew\nWB coverage raw output\ncandidate output"), wantError: true},
		{name: "coverage timeout names an incidental test only on one side", baseline: failing("WB coverage failure index:\n- [unsharded packages] TestModuleArchiveIncludesCmdWBEmbedInputs\n- [github.com/sneat-dev/wb/internal/worktrees shard 4/4] command failed without a named Go test\nWB coverage raw output\n[unsharded packages]\nok  \tgithub.com/sneat-dev/wb/internal/migrate\t29.113s\ntimed out after 8m0s\n[github.com/sneat-dev/wb/internal/worktrees shard 4/4]\n\ntimed out after 8m0s"), candidate: failing("WB coverage failure index:\n- [unsharded packages] command failed without a named Go test\n- [github.com/sneat-dev/wb/internal/worktrees shard 1/4] command failed without a named Go test\nWB coverage raw output\n[unsharded packages]\nok  \tgithub.com/sneat-dev/wb/api/githubapp\t1.494s\ntimed out after 25m0s\n[github.com/sneat-dev/wb/internal/worktrees shard 1/4]\n\ntimed out after 25m0s")},
		{name: "unnamed timeout is not accepted for a named baseline failure that did not time out", baseline: failing("WB coverage failure index:\n- [github.com/sneat-dev/wb/internal/worktrees shard 2/8] TestStable\nWB coverage raw output\n[github.com/sneat-dev/wb/internal/worktrees shard 2/8]\n--- FAIL: TestStable (0.10s)"), candidate: failing("WB coverage failure index:\n- [github.com/sneat-dev/wb/internal/worktrees shard 1/4] command failed without a named Go test\nWB coverage raw output\n[github.com/sneat-dev/wb/internal/worktrees shard 1/4]\n\ntimed out after 25m0s"), wantError: true},
		{name: "same Nx failure at different quoted snapshot paths", baseline: nodeFailing(`Could not find Nx modules at "/private/var/folders/aa/wb-worktree-merge-target-123/tree/frontend"`), candidate: nodeFailing(`Could not find Nx modules at "/private/var/folders/bb/wb-worktree-merge-target-456/tree/frontend"`)},
		{name: "different Nx failure remains different", baseline: nodeFailing(`Could not find Nx modules at "/private/var/folders/aa/wb-worktree-merge-target-123/tree/frontend"`), candidate: nodeFailing(`Could not find Nx modules at "/private/var/folders/bb/wb-worktree-merge-target-456/tree/frontend"; install dependencies first`), wantError: true},
		{name: "changed failure", baseline: failing("undefined: missing"), candidate: failing("undefined: other"), wantError: true},
		{name: "specscore environment-only baseline extra finding permits candidate", baseline: specFailing("specscore.yaml:0 studio-toolbar: requires project host/org/repo\nspec/features/x.md:12 missing-owner: owner is required"), candidate: specFailing("specscore.yaml:0 studio-toolbar: requires project host/org/repo")},
		{name: "specscore new identity", baseline: specFailing("specscore.yaml:0 studio-toolbar: requires project host/org/repo"), candidate: specFailing("specscore.yaml:0 studio-toolbar: requires project host/org/repo\nspec/features/x.md:12 missing-owner: owner is required"), wantError: true},
		{name: "specscore same identity changed detail", baseline: specFailing("specscore.yaml:0 studio-toolbar: requires project host/org/repo"), candidate: specFailing("specscore.yaml:0 studio-toolbar: now requires a configured remote")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := worktreeMergeValidationRegression(test.baseline, test.candidate)
			if (err != nil) != test.wantError {
				t.Fatalf("regression error = %v, want error=%t", err, test.wantError)
			}
		})
	}
}

func TestWorktreeMergeValidationRegressionIgnoresCoverageShardPackagePlacement(t *testing.T) {
	t.Parallel()
	detail := "WB coverage failure index:\n- [github.com/sneat-dev/wb/internal/worktrees shard 2/8] TestStable\nWB coverage raw output\noutput"
	entry := func(command string) quality.VerificationReport {
		return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
			Language: "go", Module: ".", Check: quality.CheckTest, Command: command, Status: quality.StatusFailed, Detail: detail,
		}}}
	}
	baseline := entry("go test -coverprofile … ./... (8 process-isolated shards for ./internal/worktrees)")
	candidate := entry("go test -coverprofile … ./... (8 process-isolated shards for ./cmd/wb,./internal/worktrees)")
	if err := worktreeMergeValidationRegression(baseline, candidate); err != nil {
		t.Fatalf("coverage shard package placement changed failure identity: %v", err)
	}
	candidate.Results[0].Command = "go test -race ./..."
	if err := worktreeMergeValidationRegression(baseline, candidate); err == nil {
		t.Fatal("semantic coverage command change was accepted")
	}
}

func TestWorktreeMergeValidationRegressionComparesTimeoutSourceWithoutElapsedTime(t *testing.T) {
	t.Parallel()
	const header = "WB coverage failure index:\n"
	const attempt = "- [unsharded packages] command failed without a named Go test (attempt timeout; elapsed 8m0.254192833s)\n"
	const named = "- [github.com/sneat-dev/wb/internal/orchestrate shard 1/4] TestExisting (attempt timeout; elapsed 8m0.254170416s)\n"
	const raw = "WB coverage raw output:\n[unsharded packages]\ntimed out after 8m0s\n[github.com/sneat-dev/wb/internal/orchestrate shard 1/4]\ntimed out after 8m0s\n"
	report := func(detail string) quality.VerificationReport {
		return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
			Language: "go", Module: ".", Check: quality.CheckTest, Command: "go test -coverprofile … ./...", Status: quality.StatusFailed, Detail: detail,
		}}}
	}
	baseline := report(header + attempt + named + raw)
	for _, tc := range []struct {
		name      string
		detail    string
		wantError bool
	}{
		{name: "elapsed differs", detail: header + "- [unsharded packages] command failed without a named Go test (attempt timeout; elapsed 8m0.252630334s)\n- [github.com/sneat-dev/wb/internal/orchestrate shard 1/4] TestExisting (attempt timeout; elapsed 8m0.252606292s)\n" + raw},
		{name: "timeout source changes", detail: header + "- [unsharded packages] command failed without a named Go test (check timeout; elapsed 8m0.252630334s)\n" + named + raw, wantError: true},
		{name: "named timeout source changes", detail: header + attempt + "- [github.com/sneat-dev/wb/internal/orchestrate shard 1/4] TestExisting (check timeout; elapsed 8m0.252606292s)\n" + raw, wantError: true},
		{name: "new named test fails", detail: header + attempt + "- [github.com/sneat-dev/wb/internal/orchestrate shard 1/4] TestNew (attempt timeout; elapsed 8m0.252606292s)\n" + raw, wantError: true},
		{name: "new job fails", detail: header + attempt + named + "- [github.com/sneat-dev/wb/internal/worktrees shard 2/4] command failed without a named Go test (attempt timeout; elapsed 8m0.1s)\n" + raw, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := worktreeMergeValidationRegression(baseline, report(tc.detail))
			if (err != nil) != tc.wantError {
				t.Fatalf("regression error = %v, want error=%t", err, tc.wantError)
			}
		})
	}
}

func TestWorktreeMergeValidationRegressionMatchesContactusVolatileBuildOutput(t *testing.T) {
	t.Parallel()
	nodeFailing := func(detail string) quality.VerificationReport {
		return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
			Language: "node", Module: "landings", Check: quality.CheckBuild, Command: "pnpm run build", Status: quality.StatusFailed, Detail: detail,
		}}}
	}
	baseline := nodeFailing(`$ astro build && pnpm run build:app && node scripts/assemble-app.mjs
03:53:52 [types] Generated 51ms
03:53:52 [build] output: "static"
03:53:52 [build] directory: /private/var/folders/c6/pty228l52dx19k5xfxjz1ztr0000gn/
… output truncated; final 750 bytes:
03:53:53 [vite] ✓ built in 508ms
03:53:53 ✓ Completed in 13ms.
03:53:53 [build] ✓ Completed in 540ms.
03:53:53 [node] 2 page(s) built in 611ms
$ cd ../frontend && npx nx build contactus-app --base-href=/

 NX   Could not find Nx modules at "/private/var/folders/c6/pty228l52dx19k5xfxjz1ztr0000gn/T/wb-worktree-merge-target-515900289/tree/frontend".

Have you run npm/yarn install?

[ELIFECYCLE] Command failed with exit code 1.`)
	candidate := nodeFailing(`$ astro build && pnpm run build:app && node scripts/assemble-app.mjs
03:53:43 [types] Generated 49ms
03:53:43 [build] output: "static"
03:53:43 [build] directory: /Users/alex/.wb/worktrees/merge-sneat-co-contactus-main
… output truncated; final 750 bytes:
03:53:44 [vite] ✓ built in 505ms
03:53:44 ✓ Completed in 13ms.
03:53:44 [build] ✓ Completed in 537ms.
03:53:44 [node] 2 page(s) built in 606ms
$ cd ../frontend && npx nx build contactus-app --base-href=/

 NX   Could not find Nx modules at "/Users/alex/.wb/worktrees/merge-sneat-co-contactus-main-355e0d554d15-c46e04b0fe6b/sneat-co/contactus/frontend".

Have you run npm/yarn install?

[ELIFECYCLE] Command failed with exit code 1.`)
	if err := worktreeMergeValidationRegression(baseline, candidate); err != nil {
		t.Fatalf("receipt-shaped volatile output should be equivalent: %v", err)
	}
}

func TestWorktreeMergeValidationRegressionMatchesExactContactusTruncatedTail(t *testing.T) {
	t.Parallel()
	nodeFailing := func(detail string) quality.VerificationReport {
		return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{{
			Language: "node", Module: "landings", Check: quality.CheckBuild, Command: "pnpm run build", Status: quality.StatusFailed, Detail: detail,
		}}}
	}
	baseline := nodeFailing(`$ astro build && pnpm run build:app && node scripts/assemble-app.mjs
04:56:35 [types] Generated 58ms
04:56:35 [build] output: "static"
04:56:35 [build] mode: "static"
04:56:35 [build] directory: /private/var/folders/c6/pty228l52dx19k5xfxjz1ztr0000gn/
… output truncated; final 750 bytes:
 built in 591ms
04:56:36 [vite] ✓ built in 8ms
04:56:36 [build] Rearranging server assets...

 generating static routes
04:56:36   ├─ /en/privacy/index.html (+8ms)
04:56:36   ├─ /index.html (+4ms)
04:56:36 ✓ Completed in 21ms.

04:56:36 [build] ✓ Completed in 637ms.
04:56:36 [@astrojs/sitemap] ` + "\x60" + `sitemap-index.xml` + "\x60" + ` created at ` + "\x60" + `dist` + "\x60" + `
04:56:36 [build] 2 page(s) built in 719ms
04:56:36 [build] Complete!
$ cd ../frontend && npx nx build contactus-app --base-href=/

 NX   Could not find Nx modules at "/private/var/folders/c6/pty228l52dx19k5xfxjz1ztr0000gn/T/wb-worktree-merge-target-3002203795/tree/frontend".

Have you run npm/yarn install?

[ELIFECYCLE] Command failed with exit code 1.
[ELIFECYCLE] Command failed with exit code 1.`)
	candidate := nodeFailing(`$ astro build && pnpm run build:app && node scripts/assemble-app.mjs
04:56:25 [types] Generated 27ms
04:56:25 [build] output: "static"
04:56:25 [build] mode: "static"
04:56:25 [build] directory: /Users/alex/.wb/worktrees/merge-sneat-co-contactus-main
… output truncated; final 750 bytes:
ilt in 112ms
04:56:25 [vite] ✓ built in 9ms
04:56:25 [build] Rearranging server assets...

 generating static routes
04:56:25   ├─ /en/privacy/index.html (+8ms)
04:56:25   ├─ /index.html (+4ms)
04:56:25 ✓ Completed in 20ms.

04:56:25 [build] ✓ Completed in 158ms.
04:56:25 [@astrojs/sitemap] ` + "\x60" + `sitemap-index.xml` + "\x60" + ` created at ` + "\x60" + `dist` + "\x60" + `
04:56:25 [build] 2 page(s) built in 215ms
04:56:25 [build] Complete!
$ cd ../frontend && npx nx build contactus-app --base-href=/

 NX   Could not find Nx modules at "/Users/alex/.wb/worktrees/merge-sneat-co-contactus-main-355e0d554d15-c46e04b0fe6b/sneat-co/contactus/frontend".

Have you run npm/yarn install?

[ELIFECYCLE] Command failed with exit code 1.
[ELIFECYCLE] Command failed with exit code 1.`)
	if err := worktreeMergeValidationRegression(baseline, candidate); err != nil {
		t.Fatalf("exact Contactus receipt-shaped tail should be equivalent: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(string) string
	}{
		{name: "different Nx error text", mutate: func(detail string) string {
			return strings.Replace(detail, "Could not find Nx modules", "Could not find Nx workspace", 1)
		}},
		{name: "different Nx error code", mutate: func(detail string) string {
			return strings.Replace(detail, "exit code 1", "exit code 2", 1)
		}},
		{name: "different Nx error number", mutate: func(detail string) string {
			return strings.Replace(detail, "2 page(s) built", "3 page(s) built", 1)
		}},
		{name: "different truncated timing verb", mutate: func(detail string) string {
			return strings.Replace(detail, " built in 591ms", "failed in 591ms", 1)
		}},
		{name: "extra Nx diagnostic", mutate: func(detail string) string {
			return detail + "\nNX diagnostic: install dependencies first"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if sameWorktreeMergeFailure(nodeFailing(baseline.Results[0].Detail).Results[0], nodeFailing(test.mutate(baseline.Results[0].Detail)).Results[0]) {
				t.Fatalf("normalized comparison erased %s", test.name)
			}
		})
	}
}

func TestNormalizeWorktreeMergeFailureDetailTruncatedTimingIsFailClosed(t *testing.T) {
	t.Parallel()
	const marker = "… output truncated; final 750 bytes:\n"
	for _, test := range []struct {
		name string
		in   string
		want string
	}{
		{name: "partial built", in: marker + "ilt in 112ms", want: strings.TrimSpace(marker) + " built in <duration>"},
		{name: "partial completed", in: marker + "pleted in 112ms", want: strings.TrimSpace(marker) + " completed in <duration>"},
		{name: "unknown timing phrase", in: marker + "error in 112ms", want: strings.TrimSpace(marker) + " error in 112ms"},
		{name: "semantic partial line", in: marker + "or in 112ms", want: strings.TrimSpace(marker) + " or in 112ms"},
		{name: "marker without tail line", in: marker, want: strings.TrimSpace(marker)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizeWorktreeMergeFailureDetail(test.in); got != test.want {
				t.Fatalf("normalized detail = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWorktreeMergeValidationRegressionMatchesYardiusEnvironmentFailures(t *testing.T) {
	t.Parallel()
	nodeFailing := func(detail string) quality.VerificationEntry {
		return quality.VerificationEntry{Language: "node", Module: "landings", Check: quality.CheckBuild, Command: "pnpm run build", Status: quality.StatusFailed, Detail: detail}
	}
	specFailing := func(detail string) quality.VerificationEntry {
		return quality.VerificationEntry{Language: "specscore", Module: "", Check: quality.CheckSpec, Command: "", Status: quality.StatusFailed, Detail: detail}
	}
	baseline := quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{
		nodeFailing(`$ astro build && pnpm run build:app && node scripts/assemble-app.mjs
03:54:32 [types] Generated 48ms
03:54:32 [build] directory: /private/var/folders/c6/pty228l52dx19k5xfxjz1ztr0000gn/
… output truncated; final 750 bytes:
$ cd .. && npx nx build yardius-app --base-href=/

 NX   Could not find Nx modules at "/private/var/folders/c6/pty228l52dx19k5xfxjz1ztr0000gn/T/wb-worktree-merge-target-786210721/tree".

Have you run npm/yarn install?

[ELIFECYCLE] Command failed with exit code 1.`),
		specFailing(`SpecScore config "/private/var/folders/c6/pty228l52dx19k5xfxjz1ztr0000gn/T/wb-worktree-merge-target-786210721/tree/specscore.yaml" requires root "/private/var/folders/c6/pty228l52dx19k5xfxjz1ztr0000gn/T/wb-worktree-merge-target-786210721/tree/spec", but the root is missing`),
	}}
	candidate := quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{
		nodeFailing(`$ astro build && pnpm run build:app && node scripts/assemble-app.mjs
03:54:25 [types] Generated 46ms
03:54:25 [build] directory: /Users/alex/.wb/worktrees/merge-sneat-co-yardius-main-b3d61f4f34c9-220bcc8b0858/sneat-co/yardius
… output truncated; final 750 bytes:
$ cd .. && npx nx build yardius-app --base-href=/

 NX   Could not find Nx modules at "/Users/alex/.wb/worktrees/merge-sneat-co-yardius-main-b3d61f4f34c9-220bcc8b0858/sneat-co/yardius".

Have you run npm/yarn install?

[ELIFECYCLE] Command failed with exit code 1.`),
		specFailing(`SpecScore config "/Users/alex/.wb/worktrees/merge-sneat-co-yardius-main-b3d61f4f34c9-220bcc8b0858/sneat-co/yardius/specscore.yaml" requires root "/Users/alex/.wb/worktrees/merge-sneat-co-yardius-main-b3d61f4f34c9-220bcc8b0858/sneat-co/yardius/spec", but the root is missing`),
	}}
	if err := worktreeMergeValidationRegression(baseline, candidate); err != nil {
		t.Fatalf("receipt-shaped environment failures should be equivalent: %v", err)
	}
}

func TestNormalizeWorktreeMergeFailureDetailPreservesBehaviorAndSemanticNumbers(t *testing.T) {
	t.Parallel()
	baseline := `03:53:52 [types] Generated 51ms at /private/var/folders/c6/target/tree/frontend".`
	candidate := `03:53:43 [types] Generated 49ms at /Users/alex/.wb/worktrees/candidate/tree/frontend".`
	if got, want := normalizeWorktreeMergeFailureDetail(baseline), normalizeWorktreeMergeFailureDetail(candidate); got != want {
		t.Fatalf("timestamp/duration/path-only difference normalized to %q and %q", got, want)
	}
	if got, want := normalizeWorktreeMergeFailureDetail(`Nx modules at "/private/var/folders/c6/target/tree".`), `Nx modules at "<workspace>".`; got != want {
		t.Fatalf("absolute path terminal punctuation normalization = %q, want %q", got, want)
	}
	for _, test := range []struct {
		name      string
		baseline  string
		candidate string
	}{
		{name: "semantic duration", baseline: "command timed out after 30s", candidate: "command timed out after 60s"},
		{name: "embedded timestamp", baseline: "error identity recorded at 03:53:43", candidate: "error identity recorded at 03:53:44"},
		{name: "line-leading semantic timestamp", baseline: "03:53:43 error identity", candidate: "03:53:44 error identity"},
		{name: "error code", baseline: "command failed with exit code 1", candidate: "command failed with exit code 2"},
		{name: "semantic number", baseline: "2 page(s) built", candidate: "3 page(s) built"},
		{name: "error text", baseline: "Could not find Nx modules", candidate: "Could not find Nx workspace"},
		{name: "added diagnostic", baseline: "Nx modules missing", candidate: "Nx modules missing; install dependencies first"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if left, right := normalizeWorktreeMergeFailureDetail(test.baseline), normalizeWorktreeMergeFailureDetail(test.candidate); left == right {
				t.Fatalf("normalized comparison erased %s: %q", test.name, left)
			}
		})
	}
}
