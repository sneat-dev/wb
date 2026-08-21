package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/policy"
)

// moduleOutcome is one module's result within a fleet sweep.
type moduleOutcome struct {
	Repository string `json:"repository"`
	Module     string `json:"module"`
	Directory  string `json:"-"`
	Type       string `json:"type,omitempty"`
	PolicyRef  string `json:"policy,omitempty"`

	Governed bool   `json:"governed"`
	Skipped  string `json:"skipped,omitempty"`
	Blocking int    `json:"blocking"`
	Reported int    `json:"reported"`

	Findings []policy.Finding `json:"-"`
}

// sweep walks the selected repositories and checks every Go module in them.
//
// A module with no policy declaration is recorded rather than skipped in
// silence: the quiet failure of a central policy is a repository nobody wired
// up, and a sweep that hides those reports a cleaner fleet than exists.
func sweep(repositories []deps.Repository, policyOverride string) []moduleOutcome {
	var outcomes []moduleOutcome
	for _, repository := range repositories {
		for _, moduleDir := range discoverModules(repository.Path) {
			outcome := moduleOutcome{
				Repository: repository.Slug,
				Directory:  moduleDir,
			}
			context, err := resolvePolicy(moduleDir, policyOverride)
			if err != nil {
				outcome.Skipped = strings.SplitN(err.Error(), "\n", 2)[0]
				if module, scanErr := policy.ScanModule(moduleDir); scanErr == nil {
					outcome.Module = module.Path
				}
				outcomes = append(outcomes, outcome)
				continue
			}
			outcome.Module = context.module.Path
			outcome.PolicyRef = context.loaded.Source
			outcome.Governed = true

			result, err := policy.Check(context.loaded, context.module, context.declaredType())
			if err != nil {
				outcome.Governed = false
				outcome.Skipped = err.Error()
				outcomes = append(outcomes, outcome)
				continue
			}
			if context.config.Strict {
				result.ApplyStrict()
			}
			outcome.Type = result.Type
			outcome.Blocking = result.Blocking()
			outcome.Reported = result.Reported()
			outcome.Findings = result.Findings
			outcomes = append(outcomes, outcome)
		}
	}
	sort.Slice(outcomes, func(i, j int) bool {
		if outcomes[i].Repository != outcomes[j].Repository {
			return outcomes[i].Repository < outcomes[j].Repository
		}
		return outcomes[i].Directory < outcomes[j].Directory
	})
	return outcomes
}

func fleetRepositories(match, regex string) ([]deps.Repository, error) {
	repositories, err := dependencyRepositories(nil, depsSetOptions{
		fleet:    true,
		match:    match,
		regex:    regex,
		parallel: 1,
	})
	if err != nil {
		return nil, usageError(err.Error())
	}
	return repositories, nil
}

// ---------------------------------------------------------------- report

func newDepsPolicyReportCmd() *cobra.Command {
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
			repositories, err := fleetRepositories(match, regex)
			if err != nil {
				return err
			}
			outcomes := sweep(repositories, policyFlag)
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
				return &exitError{code: exitFindings, message: fmt.Sprintf("%d blocking violation(s) across the fleet", blocking)}
			}
			return nil
		},
	}
	addFleetPolicyFlags(command, &match, &regex, &policyFlag, &format)
	return command
}

func writeReportText(out io.Writer, outcomes []moduleOutcome) {
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

// ---------------------------------------------------------------- drift

func newDepsPolicyDriftCmd() *cobra.Command {
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
			repositories, err := fleetRepositories(match, regex)
			if err != nil {
				return err
			}
			type driftRow struct {
				Repository string `json:"repository"`
				Module     string `json:"module"`
				Policy     string `json:"policy,omitempty"`
				Declared   string `json:"declaredType,omitempty"`
				Detected   string `json:"detectedType,omitempty"`
				Issue      string `json:"issue,omitempty"`
			}
			var rows []driftRow
			issues := 0
			for _, repository := range repositories {
				for _, moduleDir := range discoverModules(repository.Path) {
					row := driftRow{Repository: repository.Slug}
					context, err := resolvePolicy(moduleDir, policyFlag)
					if err != nil {
						row.Issue = "no policy: " + strings.SplitN(err.Error(), "\n", 2)[0]
						if module, scanErr := policy.ScanModule(moduleDir); scanErr == nil {
							row.Module = module.Path
						} else {
							row.Module = filepath.Base(moduleDir)
						}
						issues++
						rows = append(rows, row)
						continue
					}
					row.Module = context.module.Path
					row.Policy = context.loaded.Source
					row.Declared = context.declaredType()
					if detected, detectErr := context.loaded.Detect(context.module.Path); detectErr == nil {
						row.Detected = detected
					}
					if row.Declared != "" && row.Detected != "" && row.Declared != row.Detected {
						row.Issue = fmt.Sprintf("declared %q but detection chooses %q", row.Declared, row.Detected)
						issues++
					}
					if row.Declared == "" && row.Detected == "" {
						row.Issue = "no type declared and none detected"
						issues++
					}
					rows = append(rows, row)
				}
			}
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
				return &exitError{code: exitFindings, message: fmt.Sprintf("%d module(s) need attention", issues)}
			}
			return nil
		},
	}
	addFleetPolicyFlags(command, &match, &regex, &policyFlag, &format)
	return command
}

// ---------------------------------------------------------------- impact

func newDepsPolicyImpactCmd() *cobra.Command {
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
			candidatePath, err := filepath.Abs(args[0])
			if err != nil {
				return usageError(err.Error())
			}
			if _, err := policy.Load(candidatePath); err != nil {
				return usageError(err.Error())
			}
			repositories, err := fleetRepositories(match, regex)
			if err != nil {
				return err
			}
			baseline := sweep(repositories, "")
			candidate := sweep(repositories, candidatePath)

			type change struct {
				Repository string `json:"repository"`
				Module     string `json:"module"`
				Before     int    `json:"before"`
				After      int    `json:"after"`
			}
			index := map[string]moduleOutcome{}
			for _, outcome := range baseline {
				index[outcome.Directory] = outcome
			}
			var newlyFailing, newlyPassing, unchanged []change
			for _, outcome := range candidate {
				before, ok := index[outcome.Directory]
				if !ok || !before.Governed || !outcome.Governed {
					continue
				}
				entry := change{Repository: outcome.Repository, Module: outcome.Module, Before: before.Blocking, After: outcome.Blocking}
				switch {
				case before.Blocking == 0 && outcome.Blocking > 0:
					newlyFailing = append(newlyFailing, entry)
				case before.Blocking > 0 && outcome.Blocking == 0:
					newlyPassing = append(newlyPassing, entry)
				default:
					unchanged = append(unchanged, entry)
				}
			}

			out := cmd.OutOrStdout()
			if format == "json" {
				if err := writeJSONTo(out, map[string]any{
					"candidate":    args[0],
					"newlyFailing": newlyFailing,
					"newlyPassing": newlyPassing,
					"unchanged":    len(unchanged),
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
				_, _ = fmt.Fprintf(out, "unchanged       %d\n", len(unchanged))
			}
			if len(newlyFailing) > 0 {
				return &exitError{code: exitFindings, message: fmt.Sprintf(
					"the candidate policy would newly fail %d repositor%s", len(newlyFailing), plural(len(newlyFailing)))}
			}
			return nil
		},
	}
	command.Flags().StringVar(&match, "match", "", "select repositories whose owner/name matches this glob")
	command.Flags().StringVar(&regex, "regex", "", "select repositories whose owner/name matches this expression")
	command.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return command
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
