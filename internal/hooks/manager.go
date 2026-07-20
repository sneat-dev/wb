package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	Repair       bool
	Force        bool
	Now          func() time.Time
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

func shimContent(executable, hook, explicitConfig string) string {
	return "#!/bin/sh\nset -eu\n\n" + shimManagedSection(executable, hook, explicitConfig)
}

func shimManagedSection(executable, hook, explicitConfig string) string {
	args := []string{shellQuote(executable), "hooks", "run"}
	if explicitConfig != "" {
		args = append(args, "--config", shellQuote(expandPath(explicitConfig)))
	}
	args = append(args, shellQuote(hook), "--", `"$@"`)
	return managedStartMarker + "\n" +
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

// Check validates config, core.hooksPath, generated shims, and executability
// without changing repository state.
func Check(repoPath, configPath, wbExecutable string) (CheckReport, error) {
	policy, err := LoadPolicy(repoPath, configPath)
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
		expected := shimManagedSection(wbExecutable, name, policy.ExplicitPath)
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
	policy, err := LoadPolicy(options.RepoPath, options.ConfigPath)
	if err != nil {
		return ApplyResult{}, err
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
		expectedSection := shimManagedSection(options.WBExecutable, name, policy.ExplicitPath)
		content := shimContent(options.WBExecutable, name, policy.ExplicitPath)
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
	report, err := Check(policy.RepoRoot, options.ConfigPath, options.WBExecutable)
	if err != nil {
		return ApplyResult{}, err
	}
	result.Report = report
	return result, nil
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
