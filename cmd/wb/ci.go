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
	var format string
	var workflows, checkPatterns []string
	command := &cobra.Command{
		Use:   "wait --repo <owner/repository> --target <branch> --head <sha> [--pr <number-or-url>] [--workflow <name>]... [--check <pattern>]...",
		Short: "Wait one bounded foreground slice for checks on an exact head",
		Long: `Observe all GitHub checks for exactly one pull-request or direct-push head.

Pass --workflow (repeatable, exact GitHub Actions workflow name; two
workflows that share a name are both selected) or --check (repeatable, exact
check-run name/commit-status context, or a glob where "*" matches any run of
characters, including "/" — everything else is literal; no "?", "[...]",
escapes, or regex) to narrow the wait to a subset of the head's checks, for
example when only a release or deploy job matters and other checks are still
running. Required-check completeness is then evaluated only over the
required checks the filter selects, and the JSON result carries a "filter"
block naming what matched. What "wait for" means differs by how a check is
named: an exact --check waits only for that one job, so an unrelated sibling
job in the same Actions run — still in progress, or the whole run stuck on an
environment approval — never holds up the wait once that job itself is
terminal. A --check glob instead keeps its owning Actions run open until the
run itself finishes, since a glob can still match a job the run has not
registered yet, such as one gated by "needs:" on an earlier job in the same
run — the case #627 exists for. --workflow always waits for the whole run,
since that is its meaning regardless of --check. With several --check
patterns, a pass requires every exact (non-glob) pattern to have matched at
least one observed check, not merely one of them — a mistyped or
not-yet-registered exact name is reported pending, naming the pattern (plus
any nearby observed name it almost matches, such as a matrix job "build
(ubuntu)" or a reusable-workflow-qualified "caller / build"), rather than
letting an unrelated matched pattern wave the wait through. The same "every
name must match" rule applies to several --workflow names: one finishing
never passes the wait while another named workflow — for example one that
only starts via workflow_run after the first finishes — has produced no
observed check at all yet. While --check is active, a still-registering
Actions run with no job of its own yet is also kept open regardless of
whether anything has already matched elsewhere: an exact or glob --check
selection can be held pending by any other still-registering run on the
head, under the same or a different workflow, not only one related to what
has already matched — WB cannot know in advance which run a job will
register under. A skipped job a selected --check names is not automatically
a pass either: when its owning Actions run itself concluded failure or was
cancelled (the common shape of a job skipped because a job it "needs:"
failed), the wait reports it failed, the same verdict an unfiltered wait
reaches by observing the upstream job's own failing check-run directly. A
filter that selects nothing at all is never a vacuous pass either: it keeps
observing, at the normal cadence, until a matching check registers or the
slice ends — reported pending with "no check matching the filter has registered yet",
never a claim that the filter can never match. Never pass these flags to a
landing route (` + "`wb pr land`" + ` or a worktree merge) — landing always
evaluates the full required set.

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
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			return validateCIWaitInputs(repository, pullRequest, target, head, slice, interval, workflows, checkPatterns)
		},
		RunE: func(command *cobra.Command, args []string) error {
			machineOutput := jsonOut || format == "json"
			interactive := console.Interactive(command.ErrOrStderr(), nonInteractive)
			progress := newCIWaitProgress(progressOutput(command.ErrOrStderr(), interactive), true)
			progress.start(repository, pullRequest, target, head)
			result, err := orchestrate.WaitForCommitChecks(command.Context(), orchestrate.PullRequestWaitOptions{
				Repository: repository, PullRequest: pullRequest, Target: target, Head: strings.ToLower(head),
				Workflow: workflows, Check: checkPatterns,
				Slice: slice, CheckPollInterval: interval, Progress: progress.report, OperationProgress: progress.operationReporter("ci wait"),
			})
			if err != nil {
				progress.fail(err)
				return err
			}
			progress.finish(result)
			output := ciWaitOutput{SchemaVersion: 1, ObservedAt: time.Now().UTC(), PullRequestWaitResult: result}
			if result.Status == orchestrate.PullRequestWaitPending {
				output.ResumeArgs = ciWaitResumeArgs(repository, pullRequest, target, strings.ToLower(head), slice, interval, workflows, checkPatterns, machineOutput)
			}
			if machineOutput {
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
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json (--json is a shortcut for --format=json)")
	command.Flags().StringArrayVar(&workflows, "workflow", nil, "repeatable: restrict the wait to check runs from this exact GitHub Actions workflow name (workflows sharing a name are all selected); waits for the whole Actions run; never pass this to a landing route")
	command.Flags().StringArrayVar(&checkPatterns, "check", nil, "repeatable: restrict the wait to check-run names/commit-status contexts matching this exact name (waits only for that job; every exact pattern must match to pass), or a glob where * matches any run of characters including / and everything else is literal, no regex (waits for the owning Actions run to finish); never pass this to a landing route")
	return command
}

func validateCIWaitInputs(repository, pullRequest, target, head string, slice, interval time.Duration, workflows, checkPatterns []string) error {
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
	for _, workflow := range workflows {
		if strings.TrimSpace(workflow) == "" {
			return fmt.Errorf("--workflow must not be empty")
		}
	}
	for _, pattern := range checkPatterns {
		// Every "--check" value is syntactically valid: "*" matches any run
		// of characters (including "/"), and every other rune — including
		// "[", "]", "?" and "\" — is literal. There is no character-class,
		// escape, or "?" syntax to reject, so an exact name containing "["
		// or "\" is an ordinary literal pattern (sneat-dev/wb#627 B2,
		// red-team finding on PR #629: this used to validate with
		// path.Match, which both split "*" on "/" and rejected those
		// characters as glob syntax errors).
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("--check must not be empty")
		}
	}
	return nil
}

func ciWaitResumeArgs(repository, pullRequest, target, head string, slice, interval time.Duration, workflows, checkPatterns []string, jsonOut bool) []string {
	args := []string{"wb", "ci", "wait", "--repo", repository, "--target", target, "--head", head, "--slice", slice.String(), "--interval", interval.String()}
	if pullRequest != "" {
		args = append(args, "--pr", pullRequest)
	}
	for _, workflow := range workflows {
		args = append(args, "--workflow", workflow)
	}
	for _, pattern := range checkPatterns {
		args = append(args, "--check", pattern)
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
	if output.Filter != nil {
		if err := printCIWaitFilter(command, *output.Filter); err != nil {
			return err
		}
	}
	if len(output.ResumeArgs) > 0 {
		quoted := make([]string, 0, len(output.ResumeArgs))
		for _, argument := range output.ResumeArgs {
			quoted = append(quoted, shellQuoteCIWaitArg(argument))
		}
		_, err := fmt.Fprintf(command.OutOrStdout(), "resume: %s\n", strings.Join(quoted, " "))
		return err
	}
	for _, detail := range output.FailureDetails {
		if _, err := fmt.Fprintf(command.OutOrStdout(), "failed %s\n", detail.Check); err != nil {
			return err
		}
		if detail.RunURL != "" {
			if _, err := fmt.Fprintf(command.OutOrStdout(), "run: %s\n", detail.RunURL); err != nil {
				return err
			}
		}
		if detail.JobURL != "" {
			if _, err := fmt.Fprintf(command.OutOrStdout(), "job: %s\n", detail.JobURL); err != nil {
				return err
			}
		}
		for _, annotation := range detail.Annotations {
			location := fmt.Sprintf("%s:%d", annotation.Path, annotation.StartLine)
			if annotation.EndLine > annotation.StartLine {
				location += fmt.Sprintf("-%d", annotation.EndLine)
			}
			if _, err := fmt.Fprintf(command.OutOrStdout(), "annotation: %s: %s\n", location, annotation.Message); err != nil {
				return err
			}
		}
		if detail.Excerpt != "" {
			if _, err := fmt.Fprintf(command.OutOrStdout(), "failed-step tail:\n%s\n", detail.Excerpt); err != nil {
				return err
			}
		}
		if detail.Reason != "" {
			if _, err := fmt.Fprintf(command.OutOrStdout(), "diagnostic: %s\n", detail.Reason); err != nil {
				return err
			}
		}
	}
	return nil
}

// printCIWaitFilter reports the --workflow/--check scoping a wait applied
// (sneat-dev/wb#627): the text counterpart of the JSON "filter" block.
func printCIWaitFilter(command *cobra.Command, filter orchestrate.CheckWaitFilter) error {
	line := "filter:"
	if len(filter.Workflows) > 0 {
		line += " workflow=" + strings.Join(filter.Workflows, ",")
	}
	if len(filter.Checks) > 0 {
		line += " check=" + strings.Join(filter.Checks, ",")
	}
	line += fmt.Sprintf(" matched %d check(s), %d required check(s)", filter.MatchedChecks, filter.RequiredChecks)
	_, err := fmt.Fprintln(command.OutOrStdout(), line)
	return err
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
		target    string
	)
	cmd := &cobra.Command{
		Use:   "audit [repository-path]",
		Short: "Check coverage gates and build-artifact promotion",
		Long: `Check coverage gates and build-artifact promotion.

Pass --target <branch> to additionally compare every numeric
min_test_coverage_percent against the same workflow file on the fetched
target branch (lesson l10-coverage-floors-are-raised-with-real-tests-never-lowered-to-fit):
a threshold lower here than on the target is a coverage-floor-lowered
finding. This is a no-op when the current branch already equals --target, and
fetches origin/<target> (the one place this command is not read-only).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			code, err := runCIAudit(path, projectsRoot, filterFlag, target, fleetMode, strict, jsonOut)
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
	cmd.Flags().StringVar(&target, "target", "", "also compare coverage floors against this fetched target branch")
	addJSONFormatFlags(cmd, &jsonOut)
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

func runCIAudit(path, root, filter, target string, fleetMode, strict, jsonOut bool) (int, error) {
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
		if target != "" {
			targetFindings, err := ciaudit.CompareCoverageFloors(absolute, target)
			if err != nil {
				return 1, err
			}
			report.Findings = append(report.Findings, targetFindings...)
			sort.Slice(report.Findings, func(i, j int) bool {
				if report.Findings[i].Code == report.Findings[j].Code {
					return report.Findings[i].File < report.Findings[j].File
				}
				return report.Findings[i].Code < report.Findings[j].Code
			})
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
