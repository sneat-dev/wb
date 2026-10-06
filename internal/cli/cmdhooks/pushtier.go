package cmdhooks

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// newHooksPushTierCmd classifies one pending push into the fixed 0/1/2 tier
// contract the BuiltinGoPrePush and BuiltinNodePrePush templates dispatch on.
// The exit code IS the answer, and it deliberately falls outside wb's normal
// 0=ok/1=findings/2=usage-rejected convention (see exitOK/exitFindings/
// exitUsage in main.go) -- so this bypasses cobra's usual error-to-exit-code
// path with a direct os.Exit, the same established pattern main.go uses for
// the other hidden git-hook-adjacent helpers. The decision itself lives in
// pushTierDecision, which stays a plain function so both branches are covered
// in-process rather than only by a subprocess.
func (family commands) newHooksPushTierCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "push-tier",
		Short:  "Classify a pending push into tier 0 (skip), 1 (feature), or 2 (publication)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			code, message := family.pushTierDecision(cmd.InOrStdin())
			_, _ = io.WriteString(cmd.OutOrStdout(), message)
			family.git.Exit(code)
			return nil
		},
	}
	return cmd
}

// pushTierDecision classifies one pending push into the tier code the hook must
// exit with and the single line it narrates. Keeping the decision separate from
// the os.Exit that publishes it is what makes the failure branch -- the one
// that must never turn a classification problem into a blocked push -- testable
// without terminating the test binary.
func (family commands) pushTierDecision(stdin io.Reader) (code int, message string) {
	classification, err := family.git.Classify(stdin, ".")
	if err != nil {
		return 1, fmt.Sprintf(
			"WB hook: tier 1 — classification failed (%v); defaulting to the fast lane, CI is the real gate\n", err)
	}
	return classification.ExitCode(), fmt.Sprintf("WB hook: tier %d — %s\n", classification.ExitCode(), classification.Reason)
}
