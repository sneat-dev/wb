package quality

import "context"

// Helpers kept for tests only: no production caller remains.

// Verify runs the requested conventional Go and Node checks. The caller owns
// cross-repository parallelism; checks within one module run in the requested
// order to keep output and failures clear.
func Verify(ctx context.Context, repository, path string, checks []Check) VerificationReport {
	return VerifyWithOptions(ctx, repository, path, checks, RunOptions{})
}

func runShardedCoverage(ctx context.Context, module, outputProfile string, requestedPackages []string, shardCount int) (string, error) {
	return runShardedCoverageWithDiagnostics(ctx, module, outputProfile, requestedPackages, shardCount, "", "")
}

func runShardedCoverageWithDiagnostics(ctx context.Context, module, outputProfile string, requestedPackages []string, shardCount int, diagnosticsDir, repository string) (string, error) {
	return runShardedCoverageWithDiagnosticsAndProgress(ctx, module, outputProfile, requestedPackages, shardCount, diagnosticsDir, repository, nil)
}

func runShardedCoverageWithDiagnosticsAndProgress(ctx context.Context, module, outputProfile string, requestedPackages []string, shardCount int, diagnosticsDir, repository string, reporter func(Progress)) (string, error) {
	output, _, err := runShardedCoverageWithDiagnosticsAndProgressTimeouts(ctx, module, outputProfile, requestedPackages, shardCount, diagnosticsDir, repository, 0, 0, 0, reporter)
	return output, err
}
