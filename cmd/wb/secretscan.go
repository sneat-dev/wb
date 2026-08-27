package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/secretscan"
)

// secretOverrideFlagName and its help text are shared verbatim between
// `wb session move` and `wb session park`: the same override contract
// applies to both continuation gates.
const secretOverrideFlagName = "override-secret"

const secretOverrideFlagHelp = "acknowledge one exact secret scan finding as printed in a prior refusal (<rule-id>:<fingerprint>); repeatable, logged, never a blanket bypass"

// loadSecretScanner is a package-level seam so tests can substitute a
// deterministic scanner instead of depending on the real embedded ruleset
// plus whatever extra rules file happens to exist on the host running the
// test. Production always uses secretscan.LoadDefault, which reads
// $WB_SECRETSCAN_RULES / the user config directory.
var loadSecretScanner = func() (*secretscan.Scanner, []string, error) {
	return secretscan.LoadDefault(secretscan.LoadOptions{})
}

// scanContinuationForSecrets runs the secret scan gate against one or more
// named segments of agent-authored continuation text (a park continuation,
// or a move handover's summary/validation/remaining/body). It returns a
// non-nil error -- built by secretscan.FormatRefusal, so it never contains a
// matched value -- when any SeverityBlock finding is not covered by
// overrides. Warn-only findings (heuristic matches, and any block finding
// that WAS overridden) are returned separately so the caller can print them
// as non-fatal advisories; overriding a finding must still be visible, never
// silently dropped.
func scanContinuationForSecrets(overrides secretscan.Overrides, segments ...secretscan.Segment) ([]secretscan.Finding, error) {
	scanner, _, err := loadSecretScanner()
	if err != nil {
		return nil, fmt.Errorf("load secret scan rules: %w", err)
	}
	result := scanner.Scan(segments...)
	if blocking := result.Blocking(overrides); len(blocking) > 0 {
		return nil, secretscan.FormatRefusal(blocking)
	}
	return result.Warnings(overrides), nil
}

// printSecretScanAdvisories writes non-blocking secret-scan findings to
// stderr so they are logged and visible without failing the command: a
// heuristic (warn-only) match, or a specific finding an agent explicitly
// acknowledged via --override-secret. stdout stays reserved for the
// machine-readable result.
func printSecretScanAdvisories(command *cobra.Command, warnings []secretscan.Finding) {
	for _, finding := range warnings {
		_, _ = fmt.Fprintf(command.ErrOrStderr(), "secret scan advisory: %s\n", finding.String())
	}
}
