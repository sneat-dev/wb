package cmddeps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/sneat-dev/wb/internal/policy"
	"github.com/spf13/cobra"
)

type PolicyDependencies struct {
	Check        func(context.Context, depsrun.PolicyRequest) (policy.Result, error)
	Explain      func(context.Context, depsrun.PolicyRequest, string) (policy.Explanation, error)
	Describe     func(context.Context, depsrun.PolicyRequest) (policy.Effective, error)
	Validate     func(context.Context, string) (depsrun.PolicyValidation, error)
	Expectations func(context.Context, string) ([]policy.ExpectationResult, error)
	Init         func(context.Context, depsrun.PolicyRequest, func(depsrun.PolicyInitNotice)) (policy.Result, error)
	Report       func(context.Context, depsrun.PolicyFleetRequest) ([]depsrun.PolicyModuleOutcome, error)
	Drift        func(context.Context, depsrun.PolicyFleetRequest) (depsrun.PolicyDriftResult, error)
	Impact       func(context.Context, depsrun.PolicyFleetRequest, string) (depsrun.PolicyImpactResult, error)
}

func PolicyOperations(service *depsrun.PolicyService) PolicyDependencies {
	return PolicyDependencies{Check: service.Check, Explain: service.Explain, Describe: service.Describe, Validate: service.Validate, Expectations: service.Expectations, Init: service.Init, Report: service.Report, Drift: service.Drift, Impact: service.Impact}
}
func policyRequest(runtime shared.Runtime, dir, reference, declared string, strict bool) depsrun.PolicyRequest {
	return depsrun.PolicyRequest{Directory: dir, ProjectsRoot: runtime.Flags().ProjectsRoot, Policy: reference, DeclaredType: declared, Strict: strict}
}
func policyFleetRequest(runtime shared.Runtime, match, regex, reference string) depsrun.PolicyFleetRequest {
	flags := runtime.Flags()
	return depsrun.PolicyFleetRequest{Selection: depsrun.Selection{ProjectsRoot: flags.ProjectsRoot, Filter: flags.Filter, ExtraOrgs: flags.ExtraOrgs, Fleet: true, Parallel: 1, Match: match, Regex: regex}, Policy: reference}
}

const policyLongHelp = `Declarative dependency and layering rules.

One central policy says which kinds of repository may depend on which kinds of
dependency, and which direction imports may travel between packages inside a
repository. Every repository is held to the same document.

A repository declares only which policy governs it, and — where its module path
cannot say — what kind of repository it is:

    # ` + policy.ConfigFileName + `
    policy: acme/cicd//policy/backend.yaml
    type: extension-implementation

It may tighten its own rules with "strict: true" and it may never loosen them.
It names the policy source and never a release of it, so a tightened rule
reaches every repository at once rather than waiting for each to opt in.

The scan is lexical: import blocks and go.mod, never a resolved module graph.
No credentials, no downloads, and a verdict even when the build cannot start.`

func NewPolicy(runtime shared.Runtime, operations PolicyDependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "policy",
		Short: "Check dependency and layering rules against a central policy",
		Long:  policyLongHelp,
	}
	command.AddCommand(
		newDepsPolicyCheckCmd(runtime, operations),
		newDepsPolicyExplainCmd(runtime, operations),
		newDepsPolicyShowCmd(runtime, operations),
		newDepsPolicyValidateCmd(runtime, operations),
		newDepsPolicyTestCmd(runtime, operations),
		newDepsPolicyInitCmd(runtime, operations),
		newDepsPolicyReportCmd(runtime, operations),
		newDepsPolicyDriftCmd(runtime, operations),
		newDepsPolicyImpactCmd(runtime, operations),
	)
	return command
}

func newDepsPolicyCheckCmd(runtime shared.Runtime, operations PolicyDependencies) *cobra.Command {
	var policyFlag, typeFlag, formatFlag string
	var strict bool
	command := &cobra.Command{
		Use:   "check [directory]",
		Short: "Gate one repository against its policy",
		Long: `Check the module at or above the given directory (default ".").

Exits 0 when clean, 1 when an enforcing rule is violated, and 2 when the
invocation or the policy itself is unusable. Findings from rules the policy
runs in report mode are printed and counted, and do not affect the exit code.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := policy.ParseFormat(formatFlag)
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			result, err := operations.Check(cmd.Context(), policyRequest(runtime, directoryArg(args), policyFlag, typeFlag, strict))
			if err != nil {
				return err
			}
			if err := policy.WriteResult(cmd.OutOrStdout(), result, format); err != nil {
				return err
			}
			if blocking := result.Blocking(); blocking > 0 {
				return runtime.ExitError(shared.ExitFindings, fmt.Sprintf("%d blocking violation(s)", blocking))
			}
			return nil
		},
	}
	addPolicyFlags(command, &policyFlag, &typeFlag)
	command.Flags().StringVar(&formatFlag, "format", "text", "output format: text, json or github")
	command.Flags().BoolVar(&strict, "strict", false, "treat report-mode findings as failures for this run")
	return command
}
func newDepsPolicyExplainCmd(runtime shared.Runtime, operations PolicyDependencies) *cobra.Command {
	var policyFlag, typeFlag string
	command := &cobra.Command{
		Use:   "explain <import-path> [directory]",
		Short: "Show why one import is allowed or forbidden here",
		Long: `Print the whole decision: which group matched, via which pattern and at
which position, what else would have matched, the repository's type, and the
verdict in each scope.

The also-matched list is what makes an ordering mistake findable. Groups are
first-match-wins, so a broad pattern above a narrow one silently takes every
path the narrow one was written for.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			explanation, err := operations.Explain(cmd.Context(), policyRequest(runtime, directoryArg(args[1:]), policyFlag, typeFlag, false), args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			classification := explanation.Classification
			_, _ = fmt.Fprintf(out, "import  %s\n", explanation.Import)
			_, _ = fmt.Fprintf(out, "group   %s\n", classification.Group)
			if classification.Pattern != "" {
				_, _ = fmt.Fprintf(out, "        <- pattern #%d  %q\n", classification.PatternNumber, classification.Pattern)
			}
			for _, also := range classification.AlsoMatched {
				_, _ = fmt.Fprintf(out, "        (pattern #%d %q would also match, for group %q — shadowed)\n",
					also.Number, also.Pattern, also.Group)
			}
			origin := "declared"
			if explanation.TypeDetected {
				origin = "detected from the module path"
			}
			_, _ = fmt.Fprintf(out, "repo    %s  (%s)\n", explanation.RepoType, origin)
			for _, verdict := range explanation.Scopes {
				decision := "FORBIDDEN"
				reason := fmt.Sprintf("%s is not in %s.allow", classification.Group, verdict.Scope)
				if verdict.Allowed {
					decision = "ALLOWED"
					reason = fmt.Sprintf("%s is in %s.allow", classification.Group, verdict.Scope)
					if classification.Group == policy.GroupStdlib {
						reason = "the standard library is always permitted"
					}
				}
				_, _ = fmt.Fprintf(out, "%-7s %s — %s\n", verdict.Scope, decision, reason)
			}
			return nil
		},
	}
	addPolicyFlags(command, &policyFlag, &typeFlag)
	return command
}
func newDepsPolicyShowCmd(runtime shared.Runtime, operations PolicyDependencies) *cobra.Command {
	var policyFlag, typeFlag string
	command := &cobra.Command{
		Use:   "show [directory]",
		Short: "Print the rules this repository is actually held to",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			effective, err := operations.Describe(cmd.Context(), policyRequest(runtime, directoryArg(args), policyFlag, typeFlag, false))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "policy   %s\n", effective.PolicySource)
			_, _ = fmt.Fprintf(out, "module   %s\n", effective.Module)
			origin := "declared"
			if effective.TypeDetected {
				origin = "detected"
			}
			_, _ = fmt.Fprintf(out, "type     %s  (%s)\n", effective.RepoType, origin)
			if effective.ConfigPath != "" {
				_, _ = fmt.Fprintf(out, "config   %s\n", effective.ConfigPath)
			}
			if effective.Strict {
				_, _ = fmt.Fprintf(out, "strict   on — report-mode rules fail here\n")
			}
			_, _ = fmt.Fprintln(out)
			for _, scope := range effective.Scopes {
				_, _ = fmt.Fprintf(out, "%s.allow  %s\n", scope.Scope, strings.Join(scope.Allow, " · "))
			}
			if effective.LayerOrder != "" {
				_, _ = fmt.Fprintf(out, "\nlayers   %s\n", effective.LayerMode)
				_, _ = fmt.Fprintf(out, "         %s\n", effective.LayerOrder)
				for _, edge := range effective.LayerForbid {
					reason := ""
					if edge.Reason != "" {
						reason = " — " + edge.Reason
					}
					_, _ = fmt.Fprintf(out, "         forbidden: %s -> %s%s\n", edge.From, edge.To, reason)
				}
			}
			return nil
		},
	}
	addPolicyFlags(command, &policyFlag, &typeFlag)
	return command
}
func newDepsPolicyValidateCmd(runtime shared.Runtime, operations PolicyDependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "validate <policy-file>",
		Short: "Check a policy document for mistakes that would not otherwise show",
		Long: `Load a policy and report problems in the document itself.

The one that matters most is an unreachable pattern. Classification is
first-match-wins, so a broad group declared above a narrow one takes every path
the narrow one was written for, changes every verdict downstream, and errors
nowhere.

A group no type allows is never reported: that is how a policy forbids
something.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			validation, err := operations.Validate(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			diagnostics := validation.Diagnostics
			out := cmd.OutOrStdout()
			if len(diagnostics) == 0 {
				_, _ = fmt.Fprintf(out, "%s: no problems found (%d groups, %d types)\n",
					args[0], validation.GroupCount, validation.TypeCount)
				return nil
			}
			for _, diagnostic := range diagnostics {
				_, _ = fmt.Fprintf(out, "x %s\n", diagnostic.Message)
			}
			return runtime.ExitError(shared.ExitFindings, fmt.Sprintf("%d problem(s) in %s", len(diagnostics), args[0]))
		},
	}
	return command
}
func newDepsPolicyTestCmd(runtime shared.Runtime, operations PolicyDependencies) *cobra.Command {
	command := &cobra.Command{
		Use:   "test <policy-file>",
		Short: "Run the assertions a policy makes about itself",
		Long: `Exercise the "expect:" entries in a policy document as assertions.

Classification is the part of a policy that breaks quietly: reorder two
patterns and every verdict downstream changes with nothing to show for it. A
policy is expected to carry examples of what it means, and this runs them.

A policy that declares no assertions fails, because it cannot detect a
classification regression.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			results, err := operations.Expectations(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(results) == 0 {
				_, _ = fmt.Fprintf(out, "%s declares no expectations\n", args[0])
				return runtime.ExitError(shared.ExitFindings, "a policy with no assertions cannot detect a classification regression")
			}
			failed := 0
			for _, result := range results {
				if result.Passed {
					_, _ = fmt.Fprintf(out, "ok    %s -> %s\n", result.Subject, result.Got)
					continue
				}
				failed++
				detail := fmt.Sprintf("want %s, got %s", result.Want, orNone(result.Got))
				if result.Err != "" {
					detail = result.Err
				}
				_, _ = fmt.Fprintf(out, "FAIL  %s: %s\n", result.Subject, detail)
			}
			_, _ = fmt.Fprintf(out, "\n%d assertion(s), %d passed, %d failed\n", len(results), len(results)-failed, failed)
			if failed > 0 {
				return runtime.ExitError(shared.ExitFindings, fmt.Sprintf("%d assertion(s) failed", failed))
			}
			return nil
		},
	}
	return command
}
func newDepsPolicyInitCmd(runtime shared.Runtime, operations PolicyDependencies) *cobra.Command {
	var policyFlag string
	command := &cobra.Command{
		Use:   "init [directory]",
		Short: "Write the two-line policy declaration for this repository",
		Long: `Detect the repository's type from its module path, write ` + policy.ConfigFileName + `,
and run check immediately — so adoption starts with an honest verdict rather
than a green tick.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			result, err := operations.Init(cmd.Context(), policyRequest(runtime, directoryArg(args), policyFlag, "", false), func(notice depsrun.PolicyInitNotice) {
				_, _ = fmt.Fprintf(out, "wrote %s\n  detected type: %s\n\nrunning check...\n\n", notice.Path, notice.DetectedType)
			})
			if err != nil {
				return err
			}
			if err := policy.WriteResult(out, result, policy.FormatText); err != nil {
				return err
			}
			if blocking := result.Blocking(); blocking > 0 {
				return runtime.ExitError(shared.ExitFindings, fmt.Sprintf(
					"%d blocking violation(s) — not ready to gate on this check yet", blocking))
			}
			return nil
		},
	}
	command.Flags().StringVar(&policyFlag, "policy", "", "policy source, e.g. owner/repo//path/policy.yaml")
	return command
}
func newDepsPolicyReportCmd(runtime shared.Runtime, operations PolicyDependencies) *cobra.Command {
	var match, regex, policyFlag, format string
	command := &cobra.Command{
		Use:   "report",
		Short: "Burn-down of policy findings across the fleet",
		Long: `Aggregate every module's findings by rule.

Rules the policy runs in report mode are invisible unless someone counts them.
This is the number that has to reach zero before such a rule can be promoted to
enforcing — and the command that says which repositories are keeping it there.

Exits 1 when any enforcing rule is violated anywhere.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			outcomes, err := operations.Report(cmd.Context(), policyFleetRequest(runtime, match, regex, policyFlag))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if format == "json" {
				if err := writeJSONTo(out, outcomes); err != nil {
					return err
				}
			} else {
				writeReportText(out, outcomes)
			}
			blocking := 0
			for _, outcome := range outcomes {
				blocking += outcome.Blocking
			}
			if blocking > 0 {
				return runtime.ExitError(shared.ExitFindings, fmt.Sprintf("%d blocking violation(s) across the fleet", blocking))
			}
			return nil
		},
	}
	addFleetPolicyFlags(command, &match, &regex, &policyFlag, &format)
	return command
}
func newDepsPolicyDriftCmd(runtime shared.Runtime, operations PolicyDependencies) *cobra.Command {
	var match, regex, policyFlag, format string
	command := &cobra.Command{
		Use:   "drift",
		Short: "Report which repositories are governed, and by what",
		Long: `List every Go module in the selected repositories and say whether a policy
governs it, which policy that is, and whether a declared type disagrees with
what detection would have chosen.

Because repositories cannot pin a policy release, the interesting drift is not
version drift but coverage: a module nobody wired up is held to nothing at all.

Exits 1 when any module is ungoverned or disagrees with detection.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := operations.Drift(cmd.Context(), policyFleetRequest(runtime, match, regex, policyFlag))
			if err != nil {
				return err
			}
			rows, issues := result.Rows, result.Issues
			out := cmd.OutOrStdout()
			if format == "json" {
				if err := writeJSONTo(out, rows); err != nil {
					return err
				}
			} else {
				for _, row := range rows {
					marker := "ok"
					if row.Issue != "" {
						marker = " !"
					}
					detail := row.Policy
					if row.Issue != "" {
						detail = row.Issue
					}
					_, _ = fmt.Fprintf(out, "%s  %-38s %-44s %s\n", marker, row.Repository, row.Module, detail)
				}
				_, _ = fmt.Fprintf(out, "\n%d module(s), %d needing attention\n", len(rows), issues)
			}
			if issues > 0 {
				return runtime.ExitError(shared.ExitFindings, fmt.Sprintf("%d module(s) need attention", issues))
			}
			return nil
		},
	}
	addFleetPolicyFlags(command, &match, &regex, &policyFlag, &format)
	return command
}
func newDepsPolicyImpactCmd(runtime shared.Runtime, operations PolicyDependencies) *cobra.Command {
	var match, regex, format string
	command := &cobra.Command{
		Use:   "impact <candidate-policy-file>",
		Short: "Dry-run a candidate policy across the fleet and diff the verdicts",
		Long: `Compare a candidate policy against the one each repository runs today.

Because a repository cannot pin a policy release, a tightened rule reaches
every repository at once. This puts that blast radius in the candidate's own
pull request rather than in nine repositories on a Friday morning.

Exits 1 when the candidate would newly fail any repository.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := operations.Impact(cmd.Context(), policyFleetRequest(runtime, match, regex, ""), args[0])
			if err != nil {
				return err
			}
			newlyFailing, newlyPassing, unchanged := result.NewlyFailing, result.NewlyPassing, result.Unchanged
			out := cmd.OutOrStdout()
			if format == "json" {
				if err := writeJSONTo(out, map[string]any{
					"candidate":    args[0],
					"newlyFailing": newlyFailing,
					"newlyPassing": newlyPassing,
					"unchanged":    unchanged,
				}); err != nil {
					return err
				}
			} else {
				_, _ = fmt.Fprintf(out, "newly failing   %d repositor%s\n", len(newlyFailing), plural(len(newlyFailing)))
				for _, entry := range newlyFailing {
					_, _ = fmt.Fprintf(out, "  %-38s %d -> %d violations\n", entry.Repository, entry.Before, entry.After)
				}
				_, _ = fmt.Fprintf(out, "newly passing   %d repositor%s\n", len(newlyPassing), plural(len(newlyPassing)))
				for _, entry := range newlyPassing {
					_, _ = fmt.Fprintf(out, "  %-38s %d -> %d violations\n", entry.Repository, entry.Before, entry.After)
				}
				_, _ = fmt.Fprintf(out, "unchanged       %d\n", unchanged)
			}
			if len(newlyFailing) > 0 {
				return runtime.ExitError(shared.ExitFindings, fmt.Sprintf(
					"the candidate policy would newly fail %d repositor%s", len(newlyFailing), plural(len(newlyFailing))))
			}
			return nil
		},
	}
	command.Flags().StringVar(&match, "match", "", "select repositories whose owner/name matches this glob")
	command.Flags().StringVar(&regex, "regex", "", "select repositories whose owner/name matches this expression")
	command.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return command
}
func orNone(value string) string {
	if value == "" {
		return "nothing"
	}
	return value
}
func addPolicyFlags(command *cobra.Command, policyFlag, typeFlag *string) {
	command.Flags().StringVar(policyFlag, "policy", "", "policy source, overriding the repository's own declaration")
	command.Flags().StringVar(typeFlag, "type", "", "repository type, overriding detection")
}
func directoryArg(args []string) string {
	if len(args) > 0 && args[0] != "" {
		return args[0]
	}
	return "."
}
func writeReportText(out io.Writer, outcomes []depsrun.PolicyModuleOutcome) {
	enforcing := map[string]*findingBucket{}
	reporting := map[string]*findingBucket{}
	governed, clean, ungoverned := 0, 0, 0

	for _, outcome := range outcomes {
		if !outcome.Governed {
			ungoverned++
			continue
		}
		governed++
		if outcome.Blocking == 0 && outcome.Reported == 0 {
			clean++
		}
		for _, finding := range outcome.Findings {
			target := enforcing
			if finding.Mode == policy.ModeReport {
				target = reporting
			}
			key := findingKey(finding)
			if target[key] == nil {
				target[key] = &findingBucket{repos: map[string]bool{}}
			}
			target[key].count++
			target[key].repos[outcome.Repository] = true
		}
	}

	_, _ = fmt.Fprintf(out, "%d module(s) governed, %d clean, %d not governed\n\n", governed, clean, ungoverned)
	writeBuckets(out, "enforcing", enforcing)
	writeBuckets(out, "report only", reporting)
	if ungoverned > 0 {
		_, _ = fmt.Fprintf(out, "not governed\n")
		for _, outcome := range outcomes {
			if outcome.Governed {
				continue
			}
			_, _ = fmt.Fprintf(out, "  %-40s %s\n", outcome.Repository, outcome.Skipped)
		}
	}
}

// findingBucket groups identical findings so a burn-down reads as "this many
// of this kind, in these repositories" rather than as a wall of lines.
type findingBucket struct {
	count int
	repos map[string]bool
}

func writeBuckets(out io.Writer, label string, buckets map[string]*findingBucket) {
	if len(buckets) == 0 {
		return
	}
	keys := make([]string, 0, len(buckets))
	total := 0
	for key, bucket := range buckets {
		keys = append(keys, key)
		total += bucket.count
	}
	sort.Slice(keys, func(i, j int) bool {
		if buckets[keys[i]].count != buckets[keys[j]].count {
			return buckets[keys[i]].count > buckets[keys[j]].count
		}
		return keys[i] < keys[j]
	})
	_, _ = fmt.Fprintf(out, "%s\n", label)
	for _, key := range keys {
		bucket := buckets[key]
		repositories := make([]string, 0, len(bucket.repos))
		for repository := range bucket.repos {
			repositories = append(repositories, repository)
		}
		sort.Strings(repositories)
		where := strings.Join(repositories, ", ")
		if len(repositories) > 3 {
			where = fmt.Sprintf("%d repositories", len(repositories))
		}
		_, _ = fmt.Fprintf(out, "  %-28s %4d   %s\n", key, bucket.count, where)
	}
	_, _ = fmt.Fprintf(out, "  %-28s %4d\n\n", "total", total)
}
func findingKey(finding policy.Finding) string {
	switch finding.Rule {
	case policy.RuleLayer:
		return fmt.Sprintf("%s -> %s", finding.FromRole, finding.ToRole)
	case policy.RuleImport:
		return fmt.Sprintf("%s (%s)", finding.Group, finding.Scope)
	default:
		return finding.Rule
	}
}
func plural(count int) string {
	if count == 1 {
		return "y"
	}
	return "ies"
}
func addFleetPolicyFlags(command *cobra.Command, match, regex, policyFlag, format *string) {
	command.Flags().StringVar(match, "match", "", "select repositories whose owner/name matches this glob")
	command.Flags().StringVar(regex, "regex", "", "select repositories whose owner/name matches this expression")
	command.Flags().StringVar(policyFlag, "policy", "", "policy source applied to every module, overriding their own declarations")
	command.Flags().StringVar(format, "format", "text", "output format: text or json")
}
func writeJSONTo(out io.Writer, payload any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}
