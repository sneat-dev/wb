package main

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/policy"
)

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

func newDepsPolicyCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "policy",
		Short: "Check dependency and layering rules against a central policy",
		Long:  policyLongHelp,
	}
	command.AddCommand(
		newDepsPolicyCheckCmd(),
		newDepsPolicyExplainCmd(),
		newDepsPolicyShowCmd(),
		newDepsPolicyValidateCmd(),
		newDepsPolicyTestCmd(),
		newDepsPolicyInitCmd(),
		newDepsPolicyReportCmd(),
		newDepsPolicyDriftCmd(),
		newDepsPolicyImpactCmd(),
	)
	return command
}

// resolved is everything one repository needs to be checked.
type resolved struct {
	moduleDir string
	module    policy.Module
	config    policy.RepoConfig
	loaded    policy.Policy
}

// declaredType is the type the repository named, if any.
func (r resolved) declaredType() string { return r.config.Type }

// resolvePolicy locates the module at or above dir, reads its policy config,
// and loads the policy that governs it.
func resolvePolicy(dir, policyFlag string) (resolved, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return resolved{}, usageError(err.Error())
	}
	moduleDir, err := findModuleDir(absolute)
	if err != nil {
		return resolved{}, usageError(err.Error())
	}
	config, err := policy.LoadRepoConfig(moduleDir)
	if err != nil {
		return resolved{}, usageError(err.Error())
	}
	reference := policyFlag
	if reference == "" {
		reference = config.Policy
	}
	if reference == "" {
		return resolved{}, usageError(fmt.Sprintf(
			"no policy selected: create %s in %s with a \"policy:\" line, or pass --policy",
			policy.ConfigFileName, moduleDir))
	}
	source, err := policy.ParseSource(reference)
	if err != nil {
		return resolved{}, usageError(err.Error())
	}
	path := ""
	switch source.Kind {
	case policy.SourceURL:
		path, err = fetchPolicy(source.URL)
	default:
		path, err = source.Locate(moduleDir, policySearchRoots())
	}
	if err != nil {
		return resolved{}, usageError(err.Error())
	}
	loaded, err := policy.Load(path)
	if err != nil {
		return resolved{}, usageError(err.Error())
	}
	loaded.Source = reference
	module, err := policy.ScanModule(moduleDir)
	if err != nil {
		return resolved{}, usageError(err.Error())
	}
	return resolved{moduleDir: moduleDir, module: module, config: config, loaded: loaded}, nil
}

func policySearchRoots() []string {
	if projectsRoot == "" {
		return nil
	}
	return []string{projectsRoot}
}

// findModuleDir walks up from dir looking for the go.mod that owns it.
func findModuleDir(dir string) (string, error) {
	current := dir
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no go.mod found at or above %s", dir)
		}
		current = parent
	}
}

// fetchPolicy downloads a policy document. It is not cached: the release in
// force is the caller's to decide, and a stale cache would quietly reintroduce
// the pinning that repositories are not allowed to do.
func fetchPolicy(url string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetch policy %s: %w", url, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch policy %s: %s", url, response.Status)
	}
	file, err := os.CreateTemp("", "wb-policy-*.yaml")
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	if _, err := io.Copy(file, io.LimitReader(response.Body, 1<<20)); err != nil {
		return "", err
	}
	return file.Name(), nil
}

func usageError(message string) error {
	return &exitError{code: exitUsage, message: message}
}

// ---------------------------------------------------------------- check

func newDepsPolicyCheckCmd() *cobra.Command {
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
				return usageError(err.Error())
			}
			context, err := resolvePolicy(directoryArg(args), policyFlag)
			if err != nil {
				return err
			}
			declared := typeFlag
			if declared == "" {
				declared = context.declaredType()
			}
			result, err := policy.Check(context.loaded, context.module, declared)
			if err != nil {
				return usageError(err.Error())
			}
			if strict || context.config.Strict {
				result.ApplyStrict()
			}
			if err := policy.WriteResult(cmd.OutOrStdout(), result, format); err != nil {
				return err
			}
			if blocking := result.Blocking(); blocking > 0 {
				return &exitError{code: exitFindings, message: fmt.Sprintf("%d blocking violation(s)", blocking)}
			}
			return nil
		},
	}
	addPolicyFlags(command, &policyFlag, &typeFlag)
	command.Flags().StringVar(&formatFlag, "format", "text", "output format: text, json or github")
	command.Flags().BoolVar(&strict, "strict", false, "treat report-mode findings as failures for this run")
	return command
}

// ---------------------------------------------------------------- explain

func newDepsPolicyExplainCmd() *cobra.Command {
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
			context, err := resolvePolicy(directoryArg(args[1:]), policyFlag)
			if err != nil {
				return err
			}
			declared := typeFlag
			if declared == "" {
				declared = context.declaredType()
			}
			explanation, err := policy.Explain(context.loaded, context.module.Path, declared, args[0])
			if err != nil {
				return usageError(err.Error())
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

// ---------------------------------------------------------------- show

func newDepsPolicyShowCmd() *cobra.Command {
	var policyFlag, typeFlag string
	command := &cobra.Command{
		Use:   "show [directory]",
		Short: "Print the rules this repository is actually held to",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			context, err := resolvePolicy(directoryArg(args), policyFlag)
			if err != nil {
				return err
			}
			declared := typeFlag
			if declared == "" {
				declared = context.declaredType()
			}
			effective, err := policy.Describe(context.loaded, context.module.Path, declared, context.config.Path, context.config.Strict)
			if err != nil {
				return usageError(err.Error())
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

// ---------------------------------------------------------------- validate

func newDepsPolicyValidateCmd() *cobra.Command {
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
			loaded, err := policy.Load(args[0])
			if err != nil {
				return usageError(err.Error())
			}
			diagnostics := policy.Validate(loaded)
			out := cmd.OutOrStdout()
			if len(diagnostics) == 0 {
				_, _ = fmt.Fprintf(out, "%s: no problems found (%d groups, %d types)\n",
					args[0], len(loaded.Groups), len(loaded.Types))
				return nil
			}
			for _, diagnostic := range diagnostics {
				_, _ = fmt.Fprintf(out, "x %s\n", diagnostic.Message)
			}
			return &exitError{code: exitFindings, message: fmt.Sprintf("%d problem(s) in %s", len(diagnostics), args[0])}
		},
	}
	return command
}

// ---------------------------------------------------------------- test

func newDepsPolicyTestCmd() *cobra.Command {
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
			loaded, err := policy.Load(args[0])
			if err != nil {
				return usageError(err.Error())
			}
			results := policy.RunExpectations(loaded)
			out := cmd.OutOrStdout()
			if len(results) == 0 {
				_, _ = fmt.Fprintf(out, "%s declares no expectations\n", args[0])
				return &exitError{code: exitFindings, message: "a policy with no assertions cannot detect a classification regression"}
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
				return &exitError{code: exitFindings, message: fmt.Sprintf("%d assertion(s) failed", failed)}
			}
			return nil
		},
	}
	return command
}

func orNone(value string) string {
	if value == "" {
		return "nothing"
	}
	return value
}

// ---------------------------------------------------------------- init

func newDepsPolicyInitCmd() *cobra.Command {
	var policyFlag string
	command := &cobra.Command{
		Use:   "init [directory]",
		Short: "Write the two-line policy declaration for this repository",
		Long: `Detect the repository's type from its module path, write ` + policy.ConfigFileName + `,
and run check immediately — so adoption starts with an honest verdict rather
than a green tick.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if policyFlag == "" {
				return usageError("--policy is required: name the policy that governs this repository")
			}
			context, err := resolvePolicy(directoryArg(args), policyFlag)
			if err != nil {
				return err
			}
			target := filepath.Join(context.moduleDir, policy.ConfigFileName)
			if _, err := os.Stat(target); err == nil {
				return usageError(fmt.Sprintf("%s already exists", target))
			}
			out := cmd.OutOrStdout()
			body := fmt.Sprintf("policy: %s\n", policyFlag)
			detected, detectErr := context.loaded.Detect(context.module.Path)
			if detectErr != nil {
				return usageError(fmt.Sprintf("%s\nAdd a \"type:\" line naming one of: %s",
					detectErr, strings.Join(context.loaded.TypeNames(), ", ")))
			}
			if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(out, "wrote %s\n  detected type: %s\n\nrunning check...\n\n", target, detected)

			result, err := policy.Check(context.loaded, context.module, "")
			if err != nil {
				return usageError(err.Error())
			}
			if err := policy.WriteResult(out, result, policy.FormatText); err != nil {
				return err
			}
			if blocking := result.Blocking(); blocking > 0 {
				return &exitError{code: exitFindings, message: fmt.Sprintf(
					"%d blocking violation(s) — not ready to gate on this check yet", blocking)}
			}
			return nil
		},
	}
	command.Flags().StringVar(&policyFlag, "policy", "", "policy source, e.g. owner/repo//path/policy.yaml")
	return command
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

// discoverModules lists every Go module inside a repository checkout.
func discoverModules(root string) []string {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "vendor", "node_modules", "testdata":
				return filepath.SkipDir
			}
			if path != root && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == "go.mod" {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	sort.Strings(dirs)
	return dirs
}
