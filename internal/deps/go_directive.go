package deps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/version"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
)

// DirectivePolicy is the fleet's target `go`/`toolchain` declaration: a `go`
// language directive low enough that MVS does not impose it on consumers,
// paired with the `toolchain` directive that actually builds the module.
// GoVersion is written without a "go" prefix ("1.26.0"), matching the go.mod
// directive syntax; Toolchain carries the prefix ("go1.27.0"), matching the
// toolchain directive syntax.
type DirectivePolicy struct {
	GoVersion string
	Toolchain string
}

// languageVersion returns the policy's target go.mod `go` line in toolchain
// name syntax ("go1.26.0"), suitable for go/version comparisons.
func (p DirectivePolicy) languageVersion() string { return goSyntax(p.GoVersion) }

// DirectiveVerdict is the one category a module's directive assessment lands
// in. Exactly one applies per module.
type DirectiveVerdict string

const (
	// DirectiveCompliant means the module already declares the policy's `go`
	// language version and `toolchain`.
	DirectiveCompliant DirectiveVerdict = "compliant"
	// DirectiveWouldChange means the policy is achievable but not yet
	// declared; --apply would write it.
	DirectiveWouldChange DirectiveVerdict = "would-change"
	// DirectiveCannotComply means a dependency's own `go` directive sets a
	// ceiling above the policy's target language version.
	DirectiveCannotComply DirectiveVerdict = "cannot-comply"
	// DirectiveBelowFloor means the module's current `go` directive language
	// version is already below the policy's target; raising it is a separate
	// decision this command never makes silently.
	DirectiveBelowFloor DirectiveVerdict = "below-floor"
	// DirectiveError means the module graph could not be safely or reliably
	// resolved (for example, an unpublishable local `replace` directive, or a
	// `go list` failure).
	DirectiveError DirectiveVerdict = "error"
)

// ForcingDependency names one module in the resolved build list whose own
// `go` directive sets (or ties) the ceiling that blocks compliance.
type ForcingDependency struct {
	Path      string `json:"path"`
	Version   string `json:"version"`
	GoVersion string `json:"goVersion"`
}

// DirectiveAssessment is one Go module's achievability verdict against a
// DirectivePolicy.
type DirectiveAssessment struct {
	ModuleDir        string
	ModulePath       string
	CurrentGoVersion string
	CurrentToolchain string
	// TargetGoVersion is the exact `go` directive value the command would
	// write for a would-change or (after apply) compliant module. It may be
	// higher than the policy's baseline GoVersion when a dependency's ceiling
	// still falls within the same language version (e.g. policy 1.26.0 but a
	// dependency requires 1.26.4 exactly).
	TargetGoVersion string
	Verdict         DirectiveVerdict
	Forcing         []ForcingDependency
	Detail          string
}

// AssessDirective resolves moduleDir's actual build list with `go` tooling
// and determines whether policy is achievable there, without writing
// anything to moduleDir.
func AssessDirective(ctx context.Context, moduleDir string, policy DirectivePolicy, options Options) (DirectiveAssessment, error) {
	modPath := filepath.Join(moduleDir, "go.mod")
	contents, err := os.ReadFile(modPath)
	if err != nil {
		return DirectiveAssessment{ModuleDir: moduleDir}, fmt.Errorf("read %s: %w", modPath, err)
	}
	parsed, err := modfile.Parse("go.mod", contents, nil)
	if err != nil {
		return DirectiveAssessment{ModuleDir: moduleDir}, fmt.Errorf("parse %s: %w", modPath, err)
	}
	if parsed.Module == nil || parsed.Module.Mod.Path == "" {
		return DirectiveAssessment{ModuleDir: moduleDir}, fmt.Errorf("%s has no module path", modPath)
	}
	assessment := DirectiveAssessment{ModuleDir: moduleDir, ModulePath: parsed.Module.Mod.Path}
	if parsed.Go != nil {
		assessment.CurrentGoVersion = parsed.Go.Version
	}
	if parsed.Toolchain != nil {
		assessment.CurrentToolchain = parsed.Toolchain.Name
	}

	targetLang := version.Lang(policy.languageVersion())
	if assessment.CurrentGoVersion != "" {
		currentLang := version.Lang(goSyntax(assessment.CurrentGoVersion))
		if version.Compare(currentLang, targetLang) < 0 {
			assessment.Verdict = DirectiveBelowFloor
			assessment.Detail = fmt.Sprintf(
				"go directive %s is below the %s floor this policy targets; not raising it automatically",
				assessment.CurrentGoVersion, version.Lang(policy.languageVersion()))
			return assessment, nil
		}
	}

	// A replace to a relative local path resolves against moduleDir, which
	// resolveModuleGraph's isolated scratch copy does not preserve, so that
	// module graph cannot be safely resolved there. An absolute local path
	// resolves the same regardless of the scratch copy's location and is left
	// to resolveModuleGraph.
	for _, replace := range parsed.Replace {
		if replace.New.Version == "" && !filepath.IsAbs(replace.New.Path) {
			assessment.Verdict = DirectiveError
			assessment.Detail = fmt.Sprintf(
				"cannot safely resolve the module graph: relative local replace directive %s => %s",
				replace.Old.Path, replace.New.Path)
			return assessment, nil
		}
	}

	entries, err := resolveModuleGraph(ctx, moduleDir, options)
	if err != nil {
		assessment.Verdict = DirectiveError
		assessment.Detail = "resolve module graph: " + err.Error()
		return assessment, nil
	}

	var ceilingGoVersion string
	var forcing []ForcingDependency
	for _, entry := range entries {
		if entry.Main || entry.GoVersion == "" {
			continue
		}
		switch {
		case ceilingGoVersion == "":
			ceilingGoVersion = entry.GoVersion
			forcing = []ForcingDependency{{Path: entry.Path, Version: entry.Version, GoVersion: entry.GoVersion}}
		case version.Compare(goSyntax(entry.GoVersion), goSyntax(ceilingGoVersion)) > 0:
			ceilingGoVersion = entry.GoVersion
			forcing = []ForcingDependency{{Path: entry.Path, Version: entry.Version, GoVersion: entry.GoVersion}}
		case version.Compare(goSyntax(entry.GoVersion), goSyntax(ceilingGoVersion)) == 0:
			forcing = append(forcing, ForcingDependency{Path: entry.Path, Version: entry.Version, GoVersion: entry.GoVersion})
		}
	}
	sort.Slice(forcing, func(i, j int) bool { return forcing[i].Path < forcing[j].Path })

	if ceilingGoVersion != "" && version.Compare(version.Lang(goSyntax(ceilingGoVersion)), targetLang) > 0 {
		assessment.Verdict = DirectiveCannotComply
		assessment.Forcing = forcing
		names := make([]string, 0, len(forcing))
		for _, f := range forcing {
			names = append(names, fmt.Sprintf("%s@%s declares go %s", f.Path, f.Version, f.GoVersion))
		}
		assessment.Detail = "cannot comply: " + strings.Join(names, ", ")
		return assessment, nil
	}

	targetGoVersion := policy.GoVersion
	if ceilingGoVersion != "" && version.Compare(goSyntax(ceilingGoVersion), goSyntax(targetGoVersion)) > 0 {
		// The ceiling is within the same language version (e.g. 1.26.4 for a
		// 1.26.0 baseline) but MVS still requires our own directive to be at
		// least that high, so the achievable target moves up within 1.26.x.
		targetGoVersion = ceilingGoVersion
	}
	assessment.TargetGoVersion = targetGoVersion

	if assessment.CurrentGoVersion == targetGoVersion && assessment.CurrentToolchain == policy.Toolchain {
		assessment.Verdict = DirectiveCompliant
		assessment.Detail = "compliant"
		return assessment, nil
	}
	assessment.Verdict = DirectiveWouldChange
	assessment.Detail = fmt.Sprintf("would change `go %s` -> `go %s` (toolchain %s -> %s)",
		orNoneValue(assessment.CurrentGoVersion), targetGoVersion,
		orNoneValue(assessment.CurrentToolchain), policy.Toolchain)
	return assessment, nil
}

// ApplyDirective assesses moduleDir and, only when the verdict is
// would-change, writes the policy's `go` and `toolchain` directives with
// `go mod edit`, then runs `go mod tidy` and re-assesses to prove the edit
// was not silently reverted by a forcing dependency that assessment missed.
func ApplyDirective(ctx context.Context, moduleDir string, policy DirectivePolicy, options Options) (DirectiveAssessment, error) {
	assessment, err := AssessDirective(ctx, moduleDir, policy, options)
	if err != nil {
		return assessment, err
	}
	if assessment.Verdict != DirectiveWouldChange {
		return assessment, nil
	}
	if _, _, err := runGoCommand(ctx, options, moduleDir, "mod", "edit",
		"-go="+assessment.TargetGoVersion, "-toolchain="+policy.Toolchain); err != nil {
		return assessment, fmt.Errorf("go mod edit: %w", err)
	}
	if _, _, err := runGoCommand(ctx, options, moduleDir, "mod", "tidy"); err != nil {
		return assessment, fmt.Errorf("go mod tidy after edit: %w", err)
	}
	reassessed, err := AssessDirective(ctx, moduleDir, policy, options)
	if err != nil {
		return assessment, fmt.Errorf("re-assess after go mod tidy: %w", err)
	}
	if reassessed.CurrentGoVersion != assessment.TargetGoVersion || reassessed.CurrentToolchain != policy.Toolchain {
		return assessment, fmt.Errorf(
			"go mod tidy reverted the edit: go directive is now %q (toolchain %q); a dependency forces a higher version that assessment missed",
			reassessed.CurrentGoVersion, reassessed.CurrentToolchain)
	}
	assessment.Verdict = DirectiveCompliant
	assessment.CurrentGoVersion = reassessed.CurrentGoVersion
	assessment.CurrentToolchain = reassessed.CurrentToolchain
	assessment.Detail = fmt.Sprintf("applied `go %s` / `toolchain %s` and verified with go mod tidy",
		assessment.TargetGoVersion, policy.Toolchain)
	return assessment, nil
}

// moduleListEntry is one row of `go list -m -json all`'s streamed output.
type moduleListEntry struct {
	Path      string
	Version   string
	Main      bool
	GoVersion string
}

// resolveModuleGraph resolves moduleDir's real build list using official Go
// tooling, without ever writing to moduleDir itself: it copies only the
// go.mod (and go.sum, if present) into an isolated scratch directory and
// resolves there, so `go list`'s own `-mod=mod` bookkeeping writes land on a
// throwaway copy instead of the repository being assessed.
func resolveModuleGraph(ctx context.Context, moduleDir string, options Options) ([]moduleListEntry, error) {
	scratch, err := os.MkdirTemp("", "wb-go-directive-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	modContents, err := os.ReadFile(filepath.Join(moduleDir, "go.mod"))
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(scratch, "go.mod"), modContents, 0o644); err != nil {
		return nil, err
	}
	if sumContents, err := os.ReadFile(filepath.Join(moduleDir, "go.sum")); err == nil {
		if err := os.WriteFile(filepath.Join(scratch, "go.sum"), sumContents, 0o644); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	output, _, err := runGoCommand(ctx, options, scratch, "list", "-mod=mod", "-m", "-json", "all")
	if err != nil {
		return nil, err
	}
	var entries []moduleListEntry
	decoder := json.NewDecoder(bytes.NewReader([]byte(output)))
	for decoder.More() {
		var entry moduleListEntry
		if err := decoder.Decode(&entry); err != nil {
			return nil, fmt.Errorf("decode go list -m -json output: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// goSyntax prefixes a bare go.mod-style version ("1.26.0") with "go" so it
// can be compared with go/version, which expects toolchain name syntax
// ("go1.26.0"). A value already carrying the prefix is returned unchanged.
func goSyntax(v string) string {
	if strings.HasPrefix(v, "go") {
		return v
	}
	return "go" + v
}

func orNoneValue(v string) string {
	if v == "" {
		return "(none)"
	}
	return v
}
