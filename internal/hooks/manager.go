package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

const (
	managedStartMarker = "### Start of WB managed hook ###"
	managedEndMarker   = "### End of WB managed hook ###"
)

type Finding struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

type CheckReport struct {
	RepoRoot       string              `json:"repo_root"`
	ManagedPath    string              `json:"managed_path"`
	ConfigPaths    []string            `json:"config_paths,omitempty"`
	Hooks          []string            `json:"hooks"`
	ProfilesAuto   bool                `json:"profiles_auto"`
	ActiveProfiles []ActiveProfile     `json:"active_profiles,omitempty"`
	HookBlocks     map[string][]string `json:"hook_blocks,omitempty"`
	MetricsPath    string              `json:"metrics_path,omitempty"`
	Findings       []Finding           `json:"findings,omitempty"`
}

type ApplyOptions struct {
	RepoPath     string
	ConfigPath   string
	WBExecutable string
	// ProjectsRoot is persisted in every shim so hooks keep the same checkout
	// policy when WB was invoked with a non-default --projects-root.
	ProjectsRoot string
	// WBHome is persisted in every shim so hooks preserve an explicit WB_HOME
	// and do not recreate mixed-home state after a later shell invocation.
	WBHome string
	// WBHomeAllowsLegacy marks a shim installed from the normal default layout.
	// Such a shim pins its default write home but must still guard legacy linked
	// worktrees until migration completes.
	WBHomeAllowsLegacy bool
	Repair             bool
	Force              bool
	Now                func() time.Time
}

type ApplyResult struct {
	Report  CheckReport
	Actions []string
}

func managedPath(repoRoot string) (string, error) {
	common, err := gitCommonDir(repoRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "wb-hooks"), nil
}

func expectedHookNames(policy Policy) []string {
	names := map[string]bool{}
	for name, hook := range policy.Hooks {
		if !hook.Disabled {
			names[name] = true
		}
	}
	for _, profile := range policy.ActiveProfiles {
		definition := policy.ProfileDefinitions[profile.Name]
		for name, hook := range definition.Hooks {
			if direct, exists := policy.Hooks[name]; exists && direct.Disabled {
				continue
			}
			if !hook.Disabled {
				names[name] = true
			}
		}
	}
	if policy.Metrics.Enabled {
		for _, name := range []string{"post-commit", "pre-push"} {
			if hook, exists := policy.Hooks[name]; !exists || !hook.Disabled {
				names[name] = true
			}
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func shimContent(executable, hook, explicitConfig, projectsRoot, wbHome string, wbHomeAllowsLegacy bool) string {
	return "#!/bin/sh\nset -eu\n\n" + shimManagedSection(executable, hook, explicitConfig, projectsRoot, wbHome, wbHomeAllowsLegacy)
}

func shimManagedSection(executable, hook, explicitConfig, projectsRoot, wbHome string, wbHomeAllowsLegacy bool) string {
	args := []string{shellQuote(executable)}
	if projectsRoot != "" {
		// A hook starts in the repository, outside the command that installed
		// it, so it cannot recover a caller's non-default projects root.
		// Embedding the absolute install-time root keeps worktree guards
		// consistent at checkout, commit, and push.
		args = append(args, "--projects-root", shellQuote(projectsRoot))
	}
	args = append(args, "hooks", "run")
	if explicitConfig != "" {
		args = append(args, "--config", shellQuote(expandPath(explicitConfig)))
	}
	args = append(args, shellQuote(hook), "--", `"$@"`)
	homeExport := ""
	if wbHome != "" {
		homeExport = "export WB_HOME=" + shellQuote(wbHome) + "\n"
		if wbHomeAllowsLegacy {
			// Pin the compatibility marker to the same resolved default home as
			// WB_HOME. A generic marker could leak from an ordinary shell and
			// accidentally make an explicit alternate home read legacy state.
			homeExport += "export " + wbhome.EnvMigrationCompat + "=" + shellQuote(wbHome) + "\n"
		}
	}
	return managedStartMarker + "\n" +
		homeExport +
		strings.Join(args, " ") + "\n" +
		"_wb_hook_status=$?\n" +
		"if [ \"$_wb_hook_status\" -ne 0 ]; then\n" +
		"    exit \"$_wb_hook_status\"\n" +
		"fi\n" +
		managedEndMarker + "\n"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func absoluteProjectsRoot(projectsRoot string) (string, error) {
	if projectsRoot == "" {
		return "", nil
	}
	absolute, err := filepath.Abs(expandPath(projectsRoot))
	if err != nil {
		return "", fmt.Errorf("resolve projects root: %w", err)
	}
	return filepath.Clean(absolute), nil
}

// Check validates config, core.hooksPath, generated shims, and executability
// without changing repository state.
func Check(repoPath, configPath, wbExecutable, projectsRoot string) (CheckReport, error) {
	// Apply stores the resolved executable target in a shim. Resolve it here
	// too when possible so `hooks check` compares the same dispatcher even if
	// the caller reached the current binary through a symlinked path such as
	// macOS's /var temporary directory.
	if absolute, absErr := filepath.Abs(strings.TrimSpace(wbExecutable)); absErr == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(filepath.Clean(absolute)); resolveErr == nil {
			wbExecutable = filepath.Clean(resolved)
		}
	}
	policy, err := LoadPolicy(repoPath, configPath)
	if err != nil {
		return CheckReport{}, err
	}
	projectsRoot, err = absoluteProjectsRoot(projectsRoot)
	if err != nil {
		return CheckReport{}, err
	}
	wbHome, wbHomeAllowsLegacy, err := resolvedWBHome(projectsRoot)
	if err != nil {
		return CheckReport{}, err
	}
	managed, err := managedPath(policy.RepoRoot)
	if err != nil {
		return CheckReport{}, err
	}
	names := expectedHookNames(policy)
	report := CheckReport{
		RepoRoot:       policy.RepoRoot,
		ManagedPath:    managed,
		ConfigPaths:    append([]string(nil), policy.ConfigPaths...),
		Hooks:          names,
		ProfilesAuto:   policy.ProfilesAuto,
		ActiveProfiles: append([]ActiveProfile(nil), policy.ActiveProfiles...),
		HookBlocks:     profileBlockMap(policy),
	}
	if policy.Metrics.Enabled {
		report.MetricsPath = policy.Metrics.Path
	}
	current, err := currentHooksPath(policy.RepoRoot)
	if err != nil {
		return CheckReport{}, err
	}
	if current != managed {
		message := "core.hooksPath is not configured"
		if current != "" {
			message = fmt.Sprintf("core.hooksPath points to %s", current)
		}
		report.Findings = append(report.Findings, Finding{Code: "hooks-path", Message: message, Path: current})
	}
	for _, name := range names {
		path := filepath.Join(managed, name)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			report.Findings = append(report.Findings, Finding{Code: "hook-missing", Message: fmt.Sprintf("managed %s hook is missing", name), Path: path})
			continue
		}
		expected := shimManagedSection(wbExecutable, name, policy.ExplicitPath, projectsRoot, wbHome, wbHomeAllowsLegacy)
		actual, managed, valid := extractManagedSection(string(data))
		if !managed || !valid || actual != expected {
			report.Findings = append(report.Findings, Finding{Code: "hook-stale", Message: fmt.Sprintf("managed %s hook differs from the expected shim", name), Path: path})
		}
		if info, statErr := os.Stat(path); statErr == nil && info.Mode().Perm()&0o111 == 0 {
			report.Findings = append(report.Findings, Finding{Code: "hook-not-executable", Message: fmt.Sprintf("managed %s hook is not executable", name), Path: path})
		}
	}
	entries, readErr := os.ReadDir(managed)
	if readErr == nil {
		expected := map[string]bool{}
		for _, name := range names {
			expected[name] = true
		}
		for _, entry := range entries {
			if entry.IsDir() || expected[entry.Name()] || strings.Contains(entry.Name(), ".wb-backup-") {
				continue
			}
			path := filepath.Join(managed, entry.Name())
			data, _ := os.ReadFile(path)
			if isManagedContent(string(data)) {
				report.Findings = append(report.Findings, Finding{Code: "hook-unexpected", Message: fmt.Sprintf("stale managed hook %s remains active", entry.Name()), Path: path})
			}
		}
	}
	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].Code == report.Findings[j].Code {
			return report.Findings[i].Path < report.Findings[j].Path
		}
		return report.Findings[i].Code < report.Findings[j].Code
	})
	return report, nil
}

// Apply installs or repairs WB's local shims. It never overwrites unmanaged
// hook files unless Force is set, and forced replacements are backed up.
func Apply(options ApplyOptions) (ApplyResult, error) {
	var err error
	options.WBExecutable, err = durableWBExecutable(options.WBExecutable)
	if err != nil {
		return ApplyResult{}, err
	}
	policy, err := LoadPolicy(options.RepoPath, options.ConfigPath)
	if err != nil {
		return ApplyResult{}, err
	}
	options.ProjectsRoot, err = absoluteProjectsRoot(options.ProjectsRoot)
	if err != nil {
		return ApplyResult{}, err
	}
	if options.WBHome == "" {
		options.WBHome, options.WBHomeAllowsLegacy, err = resolvedWBHome(options.ProjectsRoot)
		if err != nil {
			return ApplyResult{}, err
		}
	}
	managed, err := managedPath(policy.RepoRoot)
	if err != nil {
		return ApplyResult{}, err
	}
	current, err := currentHooksPath(policy.RepoRoot)
	if err != nil {
		return ApplyResult{}, err
	}
	if current != "" && current != managed && !options.Force {
		return ApplyResult{}, fmt.Errorf("core.hooksPath currently points to %s; migrate those hooks into WB templates, then run `wb hooks repair --force`", current)
	}
	if current == "" {
		active, err := activeDefaultHooks(policy.RepoRoot)
		if err != nil {
			return ApplyResult{}, err
		}
		if len(active) > 0 && !options.Force {
			return ApplyResult{}, fmt.Errorf("active hooks already exist in Git's default hook directory (%s); migrate them into WB templates, then run `wb hooks repair --force`", strings.Join(active, ", "))
		}
	}
	if err := os.MkdirAll(managed, 0o755); err != nil {
		return ApplyResult{}, fmt.Errorf("create managed hooks directory: %w", err)
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	result := ApplyResult{}
	names := expectedHookNames(policy)
	for _, name := range names {
		path := filepath.Join(managed, name)
		expectedSection := shimManagedSection(options.WBExecutable, name, policy.ExplicitPath, options.ProjectsRoot, options.WBHome, options.WBHomeAllowsLegacy)
		content := shimContent(options.WBExecutable, name, policy.ExplicitPath, options.ProjectsRoot, options.WBHome, options.WBHomeAllowsLegacy)
		if existing, readErr := os.ReadFile(path); readErr == nil {
			if !isManagedContent(string(existing)) {
				if !options.Force {
					return ApplyResult{}, fmt.Errorf("refusing to overwrite unmanaged hook %s; run repair with --force to back it up", path)
				}
				backup := path + ".wb-backup-" + options.Now().UTC().Format("20060102T150405Z")
				if err := os.Rename(path, backup); err != nil {
					return ApplyResult{}, fmt.Errorf("back up unmanaged hook %s: %w", path, err)
				}
				result.Actions = append(result.Actions, "backed up "+path+" to "+backup)
			} else {
				updated, err := replaceManagedSection(string(existing), expectedSection)
				if err != nil {
					return ApplyResult{}, fmt.Errorf("update managed hook %s: %w", path, err)
				}
				content = updated
			}
		} else if !os.IsNotExist(readErr) {
			return ApplyResult{}, fmt.Errorf("read managed hook %s: %w", path, readErr)
		}
		if err := writeExecutable(path, []byte(content)); err != nil {
			return ApplyResult{}, err
		}
		result.Actions = append(result.Actions, "installed "+name)
	}
	if options.Repair {
		if err := removeStaleManagedHooks(managed, names, &result.Actions); err != nil {
			return ApplyResult{}, err
		}
	}
	if current != managed {
		if err := setHooksPath(policy.RepoRoot, managed); err != nil {
			return ApplyResult{}, err
		}
		result.Actions = append(result.Actions, "configured core.hooksPath="+managed)
	}
	report, err := Check(policy.RepoRoot, options.ConfigPath, options.WBExecutable, options.ProjectsRoot)
	if err != nil {
		return ApplyResult{}, err
	}
	result.Report = report
	return result, nil
}

func durableWBExecutable(executable string) (string, error) {
	provided := strings.TrimSpace(executable)
	if provided == "" {
		return "", fmt.Errorf("refusing to install hooks without a WB executable")
	}
	absolute, err := filepath.Abs(provided)
	if err != nil {
		return "", fmt.Errorf("resolve WB executable %s: %w", executable, err)
	}
	absolute = filepath.Clean(absolute)
	if isTransientGoRunPath(absolute) {
		return "", transientExecutableError(executable)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve WB executable %s: %w", executable, err)
	}
	resolved = filepath.Clean(resolved)
	if isTransientGoRunPath(resolved) {
		return "", transientExecutableError(executable)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect WB executable %s: %w", executable, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("WB executable %s must be a regular file", executable)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("WB executable %s is not executable", executable)
	}
	return resolved, nil
}

func transientExecutableError(executable string) error {
	return fmt.Errorf("refusing to install hooks from transient go run executable %s; build or install a durable wb binary first", executable)
}

func isTransientGoRunPath(path string) bool {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	for index, part := range parts {
		if !strings.HasPrefix(part, "go-build") {
			continue
		}
		for _, descendant := range parts[index+1:] {
			if descendant == "exe" {
				return true
			}
		}
	}
	return false
}

// RefreshManagedShims upgrades an already-managed hook installation before a
// command creates a new worktree. It deliberately leaves repositories without
// WB-managed hooks alone; a conflicting or malformed managed installation
// fails the caller before it can create a split-layout checkout.
func RefreshManagedShims(repoPath, configPath, wbExecutable, projectsRoot string) (bool, error) {
	var err error
	wbExecutable, err = durableWBExecutable(wbExecutable)
	if err != nil {
		return false, err
	}
	policy, err := LoadPolicy(repoPath, configPath)
	if err != nil {
		return false, err
	}
	projectsRoot, err = absoluteProjectsRoot(projectsRoot)
	if err != nil {
		return false, err
	}
	managed, err := managedPath(policy.RepoRoot)
	if err != nil {
		return false, err
	}
	current, err := currentHooksPath(policy.RepoRoot)
	if err != nil {
		return false, err
	}
	if current != managed {
		return false, nil
	}
	wbHome, wbHomeAllowsLegacy, err := resolvedWBHome(projectsRoot)
	if err != nil {
		return false, err
	}
	for _, name := range expectedHookNames(policy) {
		path := filepath.Join(managed, name)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return false, fmt.Errorf("read managed hook %s before worktree creation: %w", name, readErr)
		}
		actual, isManaged, valid := extractManagedSection(string(data))
		if !isManaged || !valid {
			return false, fmt.Errorf("managed hook %s is malformed; run `wb hooks repair` before creating a worktree", name)
		}
		expected := shimManagedSection(wbExecutable, name, policy.ExplicitPath, projectsRoot, wbHome, wbHomeAllowsLegacy)
		info, statErr := os.Stat(path)
		if statErr != nil {
			return false, fmt.Errorf("inspect managed hook %s before worktree creation: %w", name, statErr)
		}
		if actual != expected || info.Mode().Perm()&0o111 == 0 {
			if _, applyErr := Apply(ApplyOptions{
				RepoPath: policy.RepoRoot, ConfigPath: configPath, WBExecutable: wbExecutable,
				ProjectsRoot: projectsRoot, WBHome: wbHome, WBHomeAllowsLegacy: wbHomeAllowsLegacy, Repair: true,
			}); applyErr != nil {
				return false, fmt.Errorf("refresh incompatible managed hooks before creating a worktree: %w", applyErr)
			}
			return true, nil
		}
	}
	return false, nil
}

func resolvedWBHome(projectsRoot string) (string, bool, error) {
	if projectsRoot == "" {
		return "", false, nil
	}
	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return "", false, err
	}
	return resolution.Write.Home, !resolution.Explicit, nil
}

func writeExecutable(path string, content []byte) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, 0o755); err != nil {
		return fmt.Errorf("write hook %s: %w", path, err)
	}
	if err := os.Chmod(temporary, 0o755); err != nil {
		return fmt.Errorf("chmod hook %s: %w", path, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("activate hook %s: %w", path, err)
	}
	return nil
}

func removeStaleManagedHooks(managed string, expectedNames []string, actions *[]string) error {
	expected := map[string]bool{}
	for _, name := range expectedNames {
		expected[name] = true
	}
	entries, err := os.ReadDir(managed)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || expected[entry.Name()] || strings.Contains(entry.Name(), ".wb-backup-") {
			continue
		}
		path := filepath.Join(managed, entry.Name())
		data, _ := os.ReadFile(path)
		if !isManagedContent(string(data)) {
			continue
		}
		withoutManaged, err := removeManagedSection(string(data))
		if err != nil {
			return fmt.Errorf("remove stale managed section from %s: %w", path, err)
		}
		if hasUserHookContent(withoutManaged) {
			if err := writeExecutable(path, []byte(withoutManaged)); err != nil {
				return err
			}
			*actions = append(*actions, "removed stale WB section from "+entry.Name()+" and preserved user commands")
			continue
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		*actions = append(*actions, "removed stale managed hook "+entry.Name())
	}
	return nil
}

func isManagedContent(content string) bool {
	return strings.Contains(content, managedStartMarker) || strings.Contains(content, managedEndMarker)
}

func extractManagedSection(content string) (section string, managed, valid bool) {
	start := strings.Index(content, managedStartMarker)
	end := strings.Index(content, managedEndMarker)
	if start >= 0 || end >= 0 {
		if start < 0 || end < start {
			return "", true, false
		}
		end += len(managedEndMarker)
		if end < len(content) && content[end] == '\n' {
			end++
		}
		return content[start:end], true, true
	}
	return "", false, false
}

func replaceManagedSection(content, expectedSection string) (string, error) {
	return replaceManagedSectionWith(content, expectedSection)
}

func removeManagedSection(content string) (string, error) {
	return replaceManagedSectionWith(content, "")
}

func replaceManagedSectionWith(content, replacement string) (string, error) {
	start := strings.Index(content, managedStartMarker)
	end := strings.Index(content, managedEndMarker)
	if start >= 0 || end >= 0 {
		if start < 0 || end < start {
			return "", fmt.Errorf("managed section markers are incomplete or out of order")
		}
		end += len(managedEndMarker)
		if end < len(content) && content[end] == '\n' {
			end++
		}
		return content[:start] + replacement + content[end:], nil
	}

	return "", fmt.Errorf("managed section markers are missing")
}

func hasUserHookContent(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		switch strings.TrimSpace(line) {
		case "", "#!/bin/sh", "set -eu":
			continue
		default:
			return true
		}
	}
	return false
}

func activeDefaultHooks(repoRoot string) ([]string, error) {
	common, err := gitCommonDir(repoRoot)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(common, "hooks"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var active []string
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".sample") {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.Mode().Perm()&0o111 != 0 {
			active = append(active, entry.Name())
		}
	}
	sort.Strings(active)
	return active, nil
}
