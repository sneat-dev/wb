package main

import (
	"context"
	"fmt"
	goversion "go/version"
	"io"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/deps"
)

// defaultDirectiveGoVersion and defaultDirectiveToolchain are the fleet's
// current Go directive policy: a `go` language directive low enough that
// Go's minimal version selection does not impose it on consumers, paired
// with the `toolchain` directive that actually builds first-party modules.
// See spec/features/go-directive-policy/README.md for the rationale.
const (
	defaultDirectiveGoVersion = "1.26.0"
	defaultDirectiveToolchain = "go1.27.0"
)

// defaultCodeQLCeiling is the Go toolchain GitHub's CodeQL default-setup
// scan currently pins via GOTOOLCHAIN=local. Default setup cannot switch
// toolchains, so a module whose *effective* go requirement (its own current
// directive, or a dependency's ceiling — whichever is higher; see
// DirectiveAssessment.EffectiveGoVersion) exceeds this fails the Analyze (go)
// job outright, regardless of an explicit `toolchain` line (GOTOOLCHAIN=local
// ignores it). This is the concrete failure mode the fleet policy exists to
// prevent, not a cosmetic consistency preference: bump the ceiling here when
// GitHub advances CodeQL's bundled toolchain.
const defaultCodeQLCeiling = "1.26.7"

// codeQLRisk reports whether a module's current, committed state — not the
// policy's proposed target — already exceeds what CodeQL default-setup's
// pinned local toolchain can run, and if so, a one-line explanation naming
// both versions.
func codeQLRisk(assessment deps.DirectiveAssessment, ceiling string) (bool, string) {
	effective := assessment.EffectiveGoVersion()
	if effective == "" || ceiling == "" {
		return false, ""
	}
	if goversion.Compare(goversion.Lang(goSyntaxLocal(effective)), goversion.Lang(goSyntaxLocal(ceiling))) <= 0 {
		return false, ""
	}
	return true, fmt.Sprintf("CodeQL default setup would fail here: requires go %s, pinned to go%s (GOTOOLCHAIN=local)", effective, ceiling)
}

// goSyntaxLocal mirrors internal/deps's unexported goSyntax: prefix a bare
// go.mod-style version with "go" for go/version comparisons.
func goSyntaxLocal(v string) string {
	if len(v) >= 2 && v[:2] == "go" {
		return v
	}
	return "go" + v
}

const goDirectiveLongHelp = `Assess, and (with --apply) land, the fleet's Go directive policy: a ` + "`go`" + `
language directive of ` + "`" + defaultDirectiveGoVersion + "`" + ` paired with ` + "`toolchain " + defaultDirectiveToolchain + "`" + `.

Only the ` + "`go`" + ` directive participates in Go's minimal version selection (MVS),
and MVS takes the maximum across the whole build list — a widely-consumed
module declaring a high ` + "`go`" + ` line drags every consumer to it. The ` + "`toolchain`" + `
directive is not imposed on consumers, so this pairing lets a module build
with the newer toolchain without forcing it on anyone downstream.

The policy is not always achievable: a module's own ` + "`go`" + ` directive must be at
least the maximum ` + "`go`" + ` directive declared by every dependency in its resolved
build list. Achievability is determined by resolving that build list with real
` + "`go`" + ` tooling against an isolated copy of the module's manifest — never by
grepping go.mod files — so the verdict is exact and names the forcing
dependency when the policy cannot be met.`

func newDepsGoDirectiveCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "go-directive",
		Short: "Assess and land the fleet's go/toolchain directive policy",
		Long:  goDirectiveLongHelp,
	}
	command.AddCommand(newDepsGoDirectiveCheckCmd(), newDepsGoDirectiveReportCmd())
	return command
}

func directiveFlags(command *cobra.Command, goVersion, toolchain *string, timeout *time.Duration) {
	command.Flags().StringVar(goVersion, "go-version", defaultDirectiveGoVersion, "target `go` directive")
	command.Flags().StringVar(toolchain, "toolchain", defaultDirectiveToolchain, "target `toolchain` directive")
	command.Flags().DurationVar(timeout, "timeout", 2*time.Minute, "timeout for each go subprocess")
}

// ---------------------------------------------------------------- check

func newDepsGoDirectiveCheckCmd() *cobra.Command {
	var apply bool
	var goVersion, toolchain, codeQLCeiling string
	var timeout time.Duration
	command := &cobra.Command{
		Use:   "check [directory]",
		Short: "Assess (dry-run by default; --apply lands it) every Go module at or under a directory",
		Long: `Discover every go.mod at or under the given directory (default ".") — a
repository with several modules (for example a root module plus
backend/go.mod) reports one line per module — and assess each one against the
policy.

Dry-run by default: reports "compliant", "would change ...", "cannot comply:
<dependency>@<version> declares go <version>", "go <version> is below the
<floor> floor" (left alone), or an error, and exits 1 when any module needs
attention. --apply writes the go/toolchain directives for a module whose
verdict is would-change, then runs "go mod tidy" and re-resolves to confirm
the edit was not silently reverted by a forcing dependency assessment missed.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := directoryArg(args)
			absolute, err := filepath.Abs(root)
			if err != nil {
				return usageError(err.Error())
			}
			modules := discoverModules(absolute)
			out := cmd.OutOrStdout()
			if len(modules) == 0 {
				_, _ = fmt.Fprintf(out, "no Go module found at or under %s\n", root)
				return nil
			}
			policy := deps.DirectivePolicy{GoVersion: goVersion, Toolchain: toolchain}
			options := deps.Options{Timeout: timeout, Retry: 1}
			attention := 0
			for _, moduleDir := range modules {
				label := moduleLabel(absolute, moduleDir)
				var assessment deps.DirectiveAssessment
				var assessErr error
				if apply {
					assessment, assessErr = deps.ApplyDirective(cmd.Context(), moduleDir, policy, options)
				} else {
					assessment, assessErr = deps.AssessDirective(cmd.Context(), moduleDir, policy, options)
				}
				if assessErr != nil {
					attention++
					_, _ = fmt.Fprintf(out, "x  %-40s %s\n", label, assessErr.Error())
					continue
				}
				detail := assessment.Detail
				if atRisk, note := codeQLRisk(assessment, codeQLCeiling); atRisk {
					detail += " — " + note
				}
				_, _ = fmt.Fprintf(out, "%s  %-40s %s\n", directiveMarker(assessment.Verdict), label, detail)
				if needsAttention(assessment.Verdict, apply) {
					attention++
				}
			}
			if attention > 0 {
				return &exitError{code: exitFindings, message: fmt.Sprintf("%d module(s) need attention; see the lines above", attention)}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "write the go/toolchain directives for modules that can comply (default: report only, changes nothing)")
	command.Flags().StringVar(&codeQLCeiling, "codeql-ceiling", defaultCodeQLCeiling, "Go toolchain version CodeQL default-setup's GOTOOLCHAIN=local currently pins; annotate a module whose effective go requirement exceeds it")
	directiveFlags(command, &goVersion, &toolchain, &timeout)
	return command
}

// needsAttention reports whether verdict should count against the exit code.
// Under --apply, a fresh would-change becomes compliant or fails outright
// (a non-nil error), so would-change itself is only a failure in dry-run mode
// — it is exactly the "there is a plan to land" signal --apply exists to
// consume.
func needsAttention(verdict deps.DirectiveVerdict, applying bool) bool {
	switch verdict {
	case deps.DirectiveCannotComply, deps.DirectiveError:
		return true
	case deps.DirectiveWouldChange:
		return !applying
	default:
		return false
	}
}

func moduleLabel(root, moduleDir string) string {
	relative, err := filepath.Rel(root, moduleDir)
	if err != nil || relative == "." {
		return filepath.Base(root)
	}
	return relative
}

func directiveMarker(verdict deps.DirectiveVerdict) string {
	switch verdict {
	case deps.DirectiveCompliant:
		return "-"
	case deps.DirectiveWouldChange:
		return "✓"
	case deps.DirectiveCannotComply:
		return "✗"
	case deps.DirectiveBelowFloor:
		return "▪"
	default:
		return "x"
	}
}

// ---------------------------------------------------------------- report

// directiveRow is one module's fleet-wide assessment row, projected for text
// and JSON reporting.
type directiveRow struct {
	Repository   string                   `json:"repository"`
	Module       string                   `json:"module,omitempty"`
	Verdict      string                   `json:"verdict"`
	Detail       string                   `json:"detail"`
	Forcing      []deps.ForcingDependency `json:"forcing,omitempty"`
	CodeQLAtRisk bool                     `json:"codeQLAtRisk,omitempty"`
}

const verdictNoModule = "no-module"

func newDepsGoDirectiveReportCmd() *cobra.Command {
	var match, regex, goVersion, toolchain, format, codeQLCeiling string
	var timeout time.Duration
	command := &cobra.Command{
		Use:   "report",
		Short: "Fleet-wide dry-run plan for the go/toolchain directive policy (read-only; there is no --apply)",
		Long: `Walk every discovered repository, assess every Go module in it, and print one
row per module — never one row per repository, so a multi-module repository
(for example a root module plus backend/go.mod) is fully represented and a
repository with no go.mod is reported as having no Go module.

This command never writes to any repository: landing the policy in a given
repository is the separate, single-repository "wb deps go-directive check
<directory> --apply". Exits 1 when any module cannot comply or errored, so
the cannot-comply rows read as one worklist of which upstream module needs
fixing first.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repositories, err := fleetRepositories(match, regex)
			if err != nil {
				return err
			}
			policy := deps.DirectivePolicy{GoVersion: goVersion, Toolchain: toolchain}
			options := deps.Options{Timeout: timeout, Retry: 1}
			rows := sweepDirectives(cmd.Context(), repositories, policy, options, codeQLCeiling)
			out := cmd.OutOrStdout()
			if format == "json" {
				if err := writeJSONTo(out, rows); err != nil {
					return err
				}
			} else {
				writeDirectiveReportText(out, rows)
			}
			cannotComply, errored, codeQLAtRisk := 0, 0, 0
			for _, row := range rows {
				switch row.Verdict {
				case string(deps.DirectiveCannotComply):
					cannotComply++
				case string(deps.DirectiveError):
					errored++
				}
				if row.CodeQLAtRisk {
					codeQLAtRisk++
				}
			}
			if cannotComply > 0 || errored > 0 {
				return &exitError{code: exitFindings, message: fmt.Sprintf(
					"%d module(s) cannot comply, %d module(s) errored, %d at risk under CodeQL default setup — see the report above", cannotComply, errored, codeQLAtRisk)}
			}
			return nil
		},
	}
	command.Flags().StringVar(&match, "match", "", "select repositories whose owner/name matches this glob")
	command.Flags().StringVar(&regex, "regex", "", "select repositories whose owner/name matches this expression")
	command.Flags().StringVar(&format, "format", "text", "output format: text or json")
	command.Flags().StringVar(&codeQLCeiling, "codeql-ceiling", defaultCodeQLCeiling, "Go toolchain version CodeQL default-setup's GOTOOLCHAIN=local currently pins; flags a module whose effective go requirement exceeds it")
	directiveFlags(command, &goVersion, &toolchain, &timeout)
	return command
}

// sweepDirectives walks every Go module in the selected repositories and
// assesses each one. It never applies — the fleet report has no --apply.
func sweepDirectives(ctx context.Context, repositories []deps.Repository, policy deps.DirectivePolicy, options deps.Options, codeQLCeiling string) []directiveRow {
	var rows []directiveRow
	for _, repository := range repositories {
		if repository.Path == "" {
			rows = append(rows, directiveRow{Repository: repository.Slug, Verdict: verdictNoModule, Detail: "remote-only — not cloned locally, cannot be assessed"})
			continue
		}
		modules := discoverModules(repository.Path)
		if len(modules) == 0 {
			rows = append(rows, directiveRow{Repository: repository.Slug, Verdict: verdictNoModule, Detail: "no Go module"})
			continue
		}
		for _, moduleDir := range modules {
			row := directiveRow{Repository: repository.Slug, Module: moduleLabel(repository.Path, moduleDir)}
			assessment, err := deps.AssessDirective(ctx, moduleDir, policy, options)
			if err != nil {
				row.Verdict = string(deps.DirectiveError)
				row.Detail = err.Error()
				rows = append(rows, row)
				continue
			}
			row.Module = assessment.ModulePath
			row.Verdict = string(assessment.Verdict)
			row.Detail = assessment.Detail
			row.Forcing = assessment.Forcing
			if atRisk, note := codeQLRisk(assessment, codeQLCeiling); atRisk {
				row.CodeQLAtRisk = true
				row.Detail += " — " + note
			}
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Repository != rows[j].Repository {
			return rows[i].Repository < rows[j].Repository
		}
		return rows[i].Module < rows[j].Module
	})
	return rows
}

func writeDirectiveReportText(out io.Writer, rows []directiveRow) {
	counts := map[string]int{}
	codeQLAtRisk := 0
	for _, row := range rows {
		counts[row.Verdict]++
		if row.CodeQLAtRisk {
			codeQLAtRisk++
		}
		marker := "x"
		switch deps.DirectiveVerdict(row.Verdict) {
		case deps.DirectiveCompliant:
			marker = "-"
		case deps.DirectiveWouldChange:
			marker = "✓"
		case deps.DirectiveCannotComply:
			marker = "✗"
		case deps.DirectiveBelowFloor:
			marker = "▪"
		}
		if row.Verdict == verdictNoModule {
			marker = "–"
		}
		label := row.Repository
		if row.Module != "" {
			label = row.Repository + " (" + row.Module + ")"
		}
		_, _ = fmt.Fprintf(out, "%s  %-56s %s\n", marker, label, row.Detail)
	}
	_, _ = fmt.Fprintf(out, "\n%d module(s): ", len(rows))
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", counts[key], key))
	}
	for i, part := range parts {
		if i > 0 {
			_, _ = fmt.Fprint(out, ", ")
		}
		_, _ = fmt.Fprint(out, part)
	}
	_, _ = fmt.Fprintln(out)
	if codeQLAtRisk > 0 {
		_, _ = fmt.Fprintf(out, "%d module(s) at risk under CodeQL default setup's pinned GOTOOLCHAIN=local\n", codeQLAtRisk)
	}
}
