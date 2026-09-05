package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/ciaudit"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/orchestrate"
)

func newCICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "Inspect and validate CI/CD policy",
	}
	cmd.AddCommand(newCIAuditCmd())
	cmd.AddCommand(newCIWaitCmd())
	return cmd
}

const (
	defaultCIWaitSlice = 8 * time.Minute
)

var exactGitObjectID = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)
var ciWaitShellSafeArg = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

type ciWaitOutput struct {
	SchemaVersion int       `json:"schema_version"`
	ObservedAt    time.Time `json:"observed_at"`
	orchestrate.PullRequestWaitResult
	ResumeArgs []string `json:"resume_args,omitempty"`
}

// newCIWaitCmd provides a terminating foreground observation slice. It never
// creates a daemon or background process: pending is a first-class finding
// whose exact identity can be passed unchanged to the next invocation.
func newCIWaitCmd() *cobra.Command {
	var repository, pullRequest, target, head string
	var slice, interval time.Duration
	var jsonOut bool
	command := &cobra.Command{
		Use:   "wait --repo <owner/repository> --target <branch> --head <sha> [--pr <number-or-url>]",
		Short: "Wait one bounded foreground slice for checks on an exact head",
		Long: `Observe all GitHub checks for exactly one pull-request or direct-push head.

Every invocation is bounded (eight minutes by default, never ten), foreground,
and terminating. A pending result exits 1 with exact resume arguments; invoke
those again until checks pass or fail. In every mode WB reads the exact head's
GitHub check runs and commit statuses, preserving each check-run producer App.
With --pr it also re-reads that PR's head and target and corroborates GitHub's
PR check views. A direct target whose fully enumerated policy is empty and
whose complete check-run and status receipts remain empty may pass after the
same stable reread. PR mode fetches the exact target SHA, proves that SHA is an
ancestor of the candidate, and requires a server-enforced strict policy with at
least one required check. It never waits for current target CI to turn green;
the candidate may fix a red target. A same-named PR summary or legacy status
cannot satisfy a required context pinned to another GitHub App. A pass requires
GitHub's authoritative required-check policy plus a terminal reread that is
unchanged, so the first green snapshot cannot become sole merger evidence while
suites are still registering. A target advance rejects the receipt. Merge-group
observation is not implemented, so merge-queue PRs fail closed. The reread
proves bounded observed-set quiescence, not that a future optional workflow can
never appear; collect separate release evidence where the repository requires
it. This command never starts a detached watcher or background loop.`,
		Args: func(command *cobra.Command, args []string) error {
			if err := cobra.NoArgs(command, args); err != nil {
				return err
			}
			return validateCIWaitInputs(repository, pullRequest, target, head, slice, interval)
		},
		RunE: func(command *cobra.Command, args []string) error {
			interactive := console.Interactive(command.ErrOrStderr(), nonInteractive)
			progress := newCIWaitProgress(progressOutput(command.ErrOrStderr(), interactive), true)
			progress.start(repository, pullRequest, target, head)
			result, err := orchestrate.WaitForCommitChecks(command.Context(), orchestrate.PullRequestWaitOptions{
				Repository: repository, PullRequest: pullRequest, Target: target, Head: strings.ToLower(head),
				Slice: slice, CheckPollInterval: interval, Progress: progress.report,
			})
			if err != nil {
				progress.fail(err)
				return err
			}
			progress.finish(result)
			output := ciWaitOutput{SchemaVersion: 1, ObservedAt: time.Now().UTC(), PullRequestWaitResult: result}
			if result.Status == orchestrate.PullRequestWaitPending {
				output.ResumeArgs = ciWaitResumeArgs(repository, pullRequest, target, strings.ToLower(head), slice, interval, jsonOut)
			}
			if jsonOut {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(output); err != nil {
					return err
				}
			} else if err := printCIWait(command, output); err != nil {
				return err
			}
			if result.Status != orchestrate.PullRequestWaitPassed {
				return &exitError{code: exitFindings, message: "CI wait " + string(result.Status) + ": " + result.Reason}
			}
			return nil
		},
	}
	command.Flags().StringVar(&repository, "repo", "", "GitHub owner/repository containing the target")
	command.Flags().StringVar(&pullRequest, "pr", "", "optional pull request number or URL to corroborate before waiting")
	command.Flags().StringVar(&target, "target", "", "required target branch containing the exact direct-push head, or the PR base")
	command.Flags().StringVar(&head, "head", "", "required exact 40- or 64-hex Git head SHA")
	command.Flags().DurationVar(&slice, "slice", defaultCIWaitSlice, "maximum foreground observation slice (must be at most 9m)")
	command.Flags().DurationVar(&interval, "interval", orchestrate.DefaultCheckPollInterval, "foreground interval between GitHub check observations (a checks-bearing terminal set's confirming reread waits at most 15s)")
	command.Flags().BoolVar(&jsonOut, "json", false, "emit a versioned machine-readable result")
	return command
}

func validateCIWaitInputs(repository, pullRequest, target, head string, slice, interval time.Duration) error {
	owner, name, validRepository := strings.Cut(strings.TrimSpace(repository), "/")
	if !validRepository || owner == "" || name == "" || strings.Contains(name, "/") {
		return fmt.Errorf("--repo must be owner/repository")
	}
	if strings.TrimSpace(target) == "" || strings.TrimSpace(target) != target {
		return fmt.Errorf("--target is required and must not have surrounding whitespace")
	}
	if output, err := exec.Command("git", "check-ref-format", "--branch", target).CombinedOutput(); err != nil {
		return fmt.Errorf("--target must be a valid Git branch: %s", strings.TrimSpace(string(output)))
	}
	if !exactGitObjectID.MatchString(head) {
		return fmt.Errorf("--head must be an exact 40- or 64-hex Git SHA")
	}
	if slice <= 0 || slice > orchestrate.MaxForegroundCheckWaitSlice {
		return fmt.Errorf("--slice must be positive and at most %s", orchestrate.MaxForegroundCheckWaitSlice)
	}
	if interval <= 0 {
		return fmt.Errorf("--interval must be positive")
	}
	if interval >= slice {
		return fmt.Errorf("--interval must be shorter than --slice so WB can confirm a stable terminal reread")
	}
	return nil
}

func ciWaitResumeArgs(repository, pullRequest, target, head string, slice, interval time.Duration, jsonOut bool) []string {
	args := []string{"wb", "ci", "wait", "--repo", repository, "--target", target, "--head", head, "--slice", slice.String(), "--interval", interval.String()}
	if pullRequest != "" {
		args = append(args, "--pr", pullRequest)
	}
	if jsonOut {
		args = append(args, "--json")
	}
	return args
}

func printCIWait(command *cobra.Command, output ciWaitOutput) error {
	identity := output.Target + "@" + output.Head
	if output.PullRequest != "" {
		identity = "PR " + output.PullRequest + " -> " + identity
	}
	if _, err := fmt.Fprintf(command.OutOrStdout(), "%s %s %s: %s\n", output.Status, output.Repository, identity, output.Reason); err != nil {
		return err
	}
	if len(output.ResumeArgs) > 0 {
		quoted := make([]string, 0, len(output.ResumeArgs))
		for _, argument := range output.ResumeArgs {
			quoted = append(quoted, shellQuoteCIWaitArg(argument))
		}
		_, err := fmt.Fprintf(command.OutOrStdout(), "resume: %s\n", strings.Join(quoted, " "))
		return err
	}
	return nil
}

func shellQuoteCIWaitArg(value string) string {
	if ciWaitShellSafeArg.MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func newCIAuditCmd() *cobra.Command {
	var (
		fleetMode bool
		strict    bool
		jsonOut   bool
	)
	cmd := &cobra.Command{
		Use:   "audit [repository-path]",
		Short: "Check coverage gates and build-artifact promotion",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			code, err := runCIAudit(path, projectsRoot, filterFlag, fleetMode, strict, jsonOut)
			if err != nil {
				return err
			}
			if code != 0 {
				return &exitError{
					code:    code,
					message: "CI policy findings reported above; fix them or drop --strict to report without failing",
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fleetMode, "fleet", false, "audit every local repository under --projects-root")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero when policy findings exist")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	return cmd
}

// exitError reports a command that ran to completion but found problems worth
// a non-zero exit. The message must name what was found and where to look: an
// agent sees only that message and the exit code, so "failed" on its own tells
// it nothing it can act on.
type exitError struct {
	code    int
	message string
}

func (e *exitError) Error() string { return e.message }

func runCIAudit(path, root, filter string, fleetMode, strict, jsonOut bool) (int, error) {
	paths := []string{path}
	if fleetMode {
		repos, err := discover.ScanLocal(root)
		if err != nil {
			return 1, err
		}
		paths = paths[:0]
		for _, repo := range repos {
			if filter != "" && !strings.Contains(repo.Slug(), filter) {
				continue
			}
			paths = append(paths, repo.Path)
		}
	}

	reports := make([]ciaudit.Report, 0, len(paths))
	for _, repoPath := range paths {
		absolute, err := filepath.Abs(repoPath)
		if err != nil {
			return 1, err
		}
		report, err := ciaudit.Audit(absolute)
		if err != nil {
			return 1, err
		}
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].Path < reports[j].Path })

	if jsonOut {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(reports); err != nil {
			return 1, err
		}
	} else {
		printCIAudit(reports)
	}

	findings := 0
	for _, report := range reports {
		findings += len(report.Findings)
	}
	if strict && findings > 0 {
		return 1, nil
	}
	return 0, nil
}

func printCIAudit(reports []ciaudit.Report) {
	for _, report := range reports {
		fmt.Println(report.Path)
		if !report.HasGo && !report.HasFrontend && !report.HasDeploy {
			fmt.Println("  – no Go/frontend/deploy CI policy applies")
			continue
		}
		if report.HasGo && report.GoCoverageThreshold {
			fmt.Println("  ✓ Go coverage threshold")
		}
		if report.HasFrontend && report.FrontendCoverageThreshold {
			fmt.Println("  ✓ frontend coverage threshold")
		}
		if report.HasDeploy && report.ArtifactPromotion {
			fmt.Println("  ✓ deploys promote verified build artifacts")
		}
		for _, finding := range report.Findings {
			where := ""
			if finding.File != "" {
				where = " (" + finding.File + ")"
			}
			fmt.Printf("  ✗ %s: %s%s\n", finding.Code, finding.Message, where)
		}
	}
}
