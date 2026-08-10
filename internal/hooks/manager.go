package hooks

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
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
	RepoRoot       string          `json:"repo_root"`
	ManagedPath    string          `json:"managed_path"`
	ConfigPaths    []string        `json:"config_paths,omitempty"`
	Hooks          []string        `json:"hooks"`
	ProfilesAuto   bool            `json:"profiles_auto"`
	ActiveProfiles []ActiveProfile `json:"active_profiles,omitempty"`
	// ExcludedProfiles makes explicit policy exceptions auditable even though
	// they intentionally contribute no hook blocks. In particular, disabling
	// the default worktree admission guard must never look like an ordinary
	// healthy installation with no explanation.
	ExcludedProfiles []string            `json:"excluded_profiles,omitempty"`
	HookBlocks       map[string][]string `json:"hook_blocks,omitempty"`
	MetricsPath      string              `json:"metrics_path,omitempty"`
	Findings         []Finding           `json:"findings,omitempty"`
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
	// afterManagedHooksValidation is a test-only seam for ancestor-swap
	// regressions. Production callers cannot influence managed-hook mutation.
	afterManagedHooksValidation func()
	// afterManagedHooksPathValidation is a test-only seam after the repository
	// and common-directory paths have been identified but before the first
	// common-directory descriptor open.
	afterManagedHooksPathValidation func()
	// afterManagedHookRead is a test-only seam between reading one hook and
	// its later replace, backup, or stale-hook removal.
	afterManagedHookRead func(name string)
	// afterManagedHookAuthorization is a test-only seam after the final hook
	// identity check and immediately before an atomic descriptor-relative
	// mutation. It proves a late replacement is never clobbered.
	afterManagedHookAuthorization func(name string)
	// beforeHooksPathConfiguration is a test-only seam after the final
	// repository validation and before the descriptor-anchored Git child runs.
	beforeHooksPathConfiguration func()
	// afterHooksPathConfigurationAuthorization is a test-only seam after the
	// final validation and before the child consumes the retained common Git
	// directory descriptor. It proves a late .git swap cannot redirect config.
	afterHooksPathConfigurationAuthorization func()
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
	info, err := os.Lstat(common)
	if err != nil {
		return "", fmt.Errorf("inspect Git common directory %s: %w", common, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing symlinked Git common directory %s", common)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("git common directory is not a directory: %s", common)
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

func shimManagedSection(_ string, hook, explicitConfig, projectsRoot, wbHome string, wbHomeAllowsLegacy bool) string {
	// Do not persist the installer executable. A Go build, package-manager
	// launcher, or harness binary can disappear while this hook remains in
	// hundreds of repositories. Resolve WB afresh for every invocation, then
	// validate the physical target before executing it. WB_EXECUTABLE is an
	// explicit operator override; otherwise the user's current PATH is the
	// authority.
	args := []string{`"$_wb_hook_executable"`}
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
		wbRuntimeExecutableResolver() +
		strings.Join(args, " ") + "\n" +
		"_wb_hook_status=$?\n" +
		"if [ \"$_wb_hook_status\" -ne 0 ]; then\n" +
		"    exit \"$_wb_hook_status\"\n" +
		"fi\n" +
		managedEndMarker + "\n"
}

// wbRuntimeExecutableResolver is POSIX shell embedded in every managed hook.
// It deliberately contains no installer path. The physical-path check closes
// the common `PATH=.` and symlink-into-checkout injection cases while still
// allowing package-manager launchers to retarget between releases.
func wbRuntimeExecutableResolver() string {
	return `_wb_hook_error() {
    printf '%s\n' "WB hook: $1" >&2
    exit 1
}
_wb_hook_physical_file() {
    _wb_hook_path=$1
    _wb_hook_links=0
    while [ -L "$_wb_hook_path" ]; do
        _wb_hook_links=$((_wb_hook_links + 1))
        [ "$_wb_hook_links" -le 40 ] || return 1
        _wb_hook_link=$(readlink "$_wb_hook_path") || return 1
        case "$_wb_hook_link" in
            /*) _wb_hook_path=$_wb_hook_link ;;
            *) _wb_hook_path=${_wb_hook_path%/*}/$_wb_hook_link ;;
        esac
    done
    _wb_hook_directory=${_wb_hook_path%/*}
    _wb_hook_name=${_wb_hook_path##*/}
    [ "$_wb_hook_directory" != "$_wb_hook_path" ] || _wb_hook_directory=/
    (
        CDPATH= cd -P "$_wb_hook_directory" 2>/dev/null || exit 1
        _wb_hook_directory=$(pwd -P) || exit 1
        if [ "$_wb_hook_directory" = / ]; then
            printf '/%s\n' "$_wb_hook_name"
        else
            printf '%s/%s\n' "$_wb_hook_directory" "$_wb_hook_name"
        fi
    )
}
_wb_hook_repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || _wb_hook_error 'cannot resolve the repository root'
_wb_hook_repo_root=$(CDPATH= cd -P "$_wb_hook_repo_root" 2>/dev/null && pwd -P) || _wb_hook_error 'cannot resolve the physical repository root'
if [ -n "${WB_EXECUTABLE:-}" ]; then
    _wb_hook_candidate=$WB_EXECUTABLE
else
    _wb_hook_candidate=$(command -v wb 2>/dev/null || true)
fi
[ -n "$_wb_hook_candidate" ] || _wb_hook_error 'wb was not found; set WB_EXECUTABLE to an absolute installed executable or add wb to PATH'
case "$_wb_hook_candidate" in
    /*) ;;
    *) _wb_hook_error 'resolved wb executable must be an absolute path' ;;
esac
[ -f "$_wb_hook_candidate" ] || _wb_hook_error 'resolved wb executable must be a regular file'
[ -x "$_wb_hook_candidate" ] || _wb_hook_error 'resolved wb executable is not executable'
_wb_hook_executable=$(_wb_hook_physical_file "$_wb_hook_candidate") || _wb_hook_error 'cannot resolve the physical wb executable'
[ -f "$_wb_hook_executable" ] || _wb_hook_error 'physical wb executable must be a regular file'
[ -x "$_wb_hook_executable" ] || _wb_hook_error 'physical wb executable is not executable'
case "$_wb_hook_executable" in
    "$_wb_hook_repo_root"|"$_wb_hook_repo_root"/*) _wb_hook_error 'refusing a repository-local wb executable' ;;
esac
export WB_EXECUTABLE="$_wb_hook_executable"
`
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
	// Generated shims intentionally contain no executable path. Normalize the
	// current check process's launcher only for WB-source stale-build evidence;
	// shim text comparison itself is independent of installation location.
	if launcher, normalizeErr := normalizedWBLauncher(wbExecutable); normalizeErr == nil {
		wbExecutable = launcher
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
		RepoRoot:         policy.RepoRoot,
		ManagedPath:      managed,
		ConfigPaths:      append([]string(nil), policy.ConfigPaths...),
		Hooks:            names,
		ProfilesAuto:     policy.ProfilesAuto,
		ActiveProfiles:   append([]ActiveProfile(nil), policy.ActiveProfiles...),
		ExcludedProfiles: explicitlyExcludedProfiles(policy),
		HookBlocks:       profileBlockMap(policy),
	}
	if policy.Metrics.Enabled {
		report.MetricsPath = policy.Metrics.Path
	}
	if err := validateManagedHooksDirectory(managed); err != nil {
		report.Findings = append(report.Findings, Finding{
			Code: "managed-hooks-path", Message: err.Error(), Path: managed,
		})
		return report, nil
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
	// A shim can be perfectly correct text and still enforce outdated policy
	// if the executable it invokes hasn't been rebuilt since. That's only a
	// meaningful comparison when the checked repository is wb's own source:
	// its HEAD is the branch a dogfooded, branch-specific wb build is meant to
	// track. For any other managed repository, wb's own build time has no
	// relationship to that repository's commit history — comparing them would
	// flag nearly every real installation, since a stable wb build is almost
	// always older than the next commit made in an actively developed repo.
	var headCommitTime time.Time
	var headTimeErr error
	var checkedExecutablePath string
	var checkedExecutableInfo os.FileInfo
	checkExecutableStaleness := repositoryIsWBSourceModule(policy.RepoRoot)
	if checkExecutableStaleness {
		headCommitTime, headTimeErr = repositoryHeadCommitTime(policy.RepoRoot)
		if resolved, resolveErr := filepath.EvalSymlinks(wbExecutable); resolveErr == nil {
			if info, statErr := os.Stat(resolved); statErr == nil && info.Mode().IsRegular() {
				checkedExecutablePath = filepath.Clean(resolved)
				checkedExecutableInfo = info
			}
		}
	}
	for _, name := range names {
		path := filepath.Join(managed, name)
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			report.Findings = append(report.Findings, Finding{Code: "hook-missing", Message: fmt.Sprintf("managed %s hook is missing", name), Path: path})
			continue
		}
		expected := shimManagedSection(wbExecutable, name, policy.ExplicitPath, projectsRoot, wbHome, wbHomeAllowsLegacy)
		actual, isManaged, valid := extractManagedSection(string(data))
		if !isManaged || !valid || actual != expected {
			report.Findings = append(report.Findings, Finding{Code: "hook-stale", Message: fmt.Sprintf("managed %s hook differs from the expected shim", name), Path: path})
		}
		if info, statErr := os.Stat(path); statErr == nil && info.Mode().Perm()&0o111 == 0 {
			report.Findings = append(report.Findings, Finding{Code: "hook-not-executable", Message: fmt.Sprintf("managed %s hook is not executable", name), Path: path})
		}
		if checkExecutableStaleness && isManaged && valid && headTimeErr == nil && checkedExecutableInfo != nil && checkedExecutableInfo.ModTime().Before(headCommitTime) {
			report.Findings = append(report.Findings, Finding{
				Code: "hook-executable-stale",
				Message: fmt.Sprintf(
					"the WB executable checking managed %s was built %s, before HEAD was committed at %s — rebuild it to enforce current policy",
					name, checkedExecutableInfo.ModTime().Format(time.RFC3339), headCommitTime.Format(time.RFC3339),
				),
				Path: checkedExecutablePath,
			})
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

func explicitlyExcludedProfiles(policy Policy) []string {
	profiles := make([]string, 0)
	for name, selected := range policy.ProfileSelections {
		if !selected {
			profiles = append(profiles, name)
		}
	}
	sort.Strings(profiles)
	return profiles
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
	managedDirectory, err := openManagedHooksDirectory(policy.RepoRoot, managed, options.afterManagedHooksPathValidation)
	if err != nil {
		return ApplyResult{}, err
	}
	defer managedDirectory.close()
	if options.afterManagedHooksValidation != nil {
		options.afterManagedHooksValidation()
	}
	if err := managedDirectory.validate(); err != nil {
		return ApplyResult{}, err
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	result := ApplyResult{}
	names := expectedHookNames(policy)
	for _, name := range names {
		if err := managedDirectory.validate(); err != nil {
			return ApplyResult{}, err
		}
		expectedSection := shimManagedSection(options.WBExecutable, name, policy.ExplicitPath, options.ProjectsRoot, options.WBHome, options.WBHomeAllowsLegacy)
		content := shimContent(options.WBExecutable, name, policy.ExplicitPath, options.ProjectsRoot, options.WBHome, options.WBHomeAllowsLegacy)
		needsWrite := true
		existing, readErr := readManagedHook(managedDirectory.directory, name)
		expectedIdentity := absentManagedHookIdentity()
		if readErr == nil {
			expectedIdentity = existing.identity
			if options.afterManagedHookRead != nil {
				options.afterManagedHookRead(name)
			}
			if !isManagedContent(string(existing.content)) {
				if !options.Force {
					return ApplyResult{}, fmt.Errorf("refusing to overwrite unmanaged hook %s; run repair with --force to back it up", filepath.Join(managed, name))
				}
				backupName := name + ".wb-backup-" + options.Now().UTC().Format("20060102T150405Z")
				if err := backupManagedHook(managedDirectory, name, backupName, existing.identity, options.afterManagedHookAuthorization); err != nil {
					return ApplyResult{}, err
				}
				expectedIdentity = absentManagedHookIdentity()
				result.Actions = append(result.Actions, "backed up "+filepath.Join(managed, name)+" to "+filepath.Join(managed, backupName))
			} else {
				updated, err := replaceManagedSection(string(existing.content), expectedSection)
				if err != nil {
					return ApplyResult{}, fmt.Errorf("update managed hook %s: %w", filepath.Join(managed, name), err)
				}
				content = updated
				if updated == string(existing.content) && existing.mode.Perm()&0o111 != 0 {
					needsWrite = false
				}
			}
		} else if !os.IsNotExist(readErr) {
			return ApplyResult{}, fmt.Errorf("read managed hook %s: %w", filepath.Join(managed, name), readErr)
		}
		if needsWrite {
			if err := writeExecutableAt(managedDirectory, name, []byte(content), expectedIdentity, options.afterManagedHookAuthorization); err != nil {
				return ApplyResult{}, err
			}
			result.Actions = append(result.Actions, "installed "+name)
		}
	}
	if options.Repair {
		if err := managedDirectory.validate(); err != nil {
			return ApplyResult{}, err
		}
		if err := removeStaleManagedHooksAt(managedDirectory, names, &result.Actions, options.afterManagedHookRead, options.afterManagedHookAuthorization); err != nil {
			return ApplyResult{}, err
		}
	}
	if current != managed {
		if err := managedDirectory.validate(); err != nil {
			return ApplyResult{}, err
		}
		if options.beforeHooksPathConfiguration != nil {
			options.beforeHooksPathConfiguration()
		}
		if err := managedDirectory.validate(); err != nil {
			return ApplyResult{}, err
		}
		if options.afterHooksPathConfigurationAuthorization != nil {
			options.afterHooksPathConfigurationAuthorization()
		}
		if err := setHooksPathAt(managedDirectory.repo, managedDirectory.common, managed); err != nil {
			return ApplyResult{}, err
		}
		result.Actions = append(result.Actions, "configured core.hooksPath="+managed)
	}
	if err := managedDirectory.validate(); err != nil {
		return ApplyResult{}, err
	}
	report, err := Check(policy.RepoRoot, options.ConfigPath, options.WBExecutable, options.ProjectsRoot)
	if err != nil {
		return ApplyResult{}, err
	}
	if len(report.Findings) > 0 {
		return ApplyResult{}, fmt.Errorf("managed hooks remain unhealthy after installation: %d finding(s); run `wb hooks check` for details", len(report.Findings))
	}
	result.Report = report
	return result, nil
}

func durableWBExecutable(executable string) (string, error) {
	launcher, err := normalizedWBLauncher(executable)
	if err != nil {
		return "", err
	}
	if isTransientGoRunPath(launcher) {
		return "", transientExecutableError(executable)
	}
	// Validate the final target, but retain the normalised launcher path in
	// generated shims. Package-manager launchers intentionally retarget on
	// upgrade; persisting the resolved Caskroom path would strand hooks on the
	// old release until a manual repair.
	resolved, err := filepath.EvalSymlinks(launcher)
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
	return launcher, nil
}

func normalizedWBLauncher(executable string) (string, error) {
	provided := strings.TrimSpace(executable)
	if provided == "" {
		return "", fmt.Errorf("refusing to install hooks without a WB executable")
	}
	absolute, err := filepath.Abs(provided)
	if err != nil {
		return "", fmt.Errorf("resolve WB executable %s: %w", executable, err)
	}
	return filepath.Clean(absolute), nil
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
	configured, err := configuredHooksPath(policy.RepoRoot)
	if err != nil {
		return false, err
	}
	// Validate the lexical WB-managed location before resolving the configured
	// path. Otherwise .git/wb-hooks -> /outside resolves away from `managed`,
	// looks unmanaged, and incorrectly takes the no-op early return below.
	if configured != "" && filepath.Clean(configured) == filepath.Clean(managed) {
		if err := validateManagedHooksDirectory(managed); err != nil {
			return false, err
		}
	}
	current, err := currentHooksPath(policy.RepoRoot)
	if err != nil {
		return false, err
	}
	if current != managed {
		return false, nil
	}
	if err := validateManagedHooksDirectory(managed); err != nil {
		return false, err
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

// managedHooksDirectory retains the repository, Git common-directory, and
// WB-owned hooks-directory descriptors while Apply mutates hook files. The
// lexical paths remain only for diagnostics and identity checks; writes,
// renames, and removals use the held descriptors.
type managedHooksDirectory struct {
	path       string
	commonPath string
	repoPath   string
	repo       *os.File
	common     *os.File
	directory  *os.File
}

type managedHookIdentity struct {
	exists bool
	device uint64
	inode  uint64
}

type managedHookSnapshot struct {
	content  []byte
	identity managedHookIdentity
	mode     os.FileMode
}

func absentManagedHookIdentity() managedHookIdentity { return managedHookIdentity{} }

func openManagedHooksDirectory(repoRoot, managed string, afterPathValidation func()) (managedHooksDirectory, error) {
	commonPath := filepath.Dir(managed)
	result := managedHooksDirectory{path: managed, commonPath: commonPath, repoPath: repoRoot}
	if repoRoot != "" {
		repo, err := openAbsoluteHooksDirectoryNoFollow(repoRoot)
		if err != nil {
			return managedHooksDirectory{}, fmt.Errorf("open repository directory %s without following links: %w", repoRoot, err)
		}
		result.repo = repo
	}
	commonBefore, err := inspectHooksDirectoryPath(commonPath, "Git common directory")
	if err != nil {
		result.close()
		return managedHooksDirectory{}, err
	}
	if result.repo != nil && !managedDirectoryPathMatches(result.repoPath, result.repo) {
		result.close()
		return managedHooksDirectory{}, fmt.Errorf("repository directory path changed while managing hooks: %s", result.repoPath)
	}
	if afterPathValidation != nil {
		afterPathValidation()
	}
	common, err := openAbsoluteHooksDirectoryNoFollow(commonPath)
	if err != nil {
		result.close()
		return managedHooksDirectory{}, fmt.Errorf("open Git common directory %s without following links: %w", commonPath, err)
	}
	result.common = common
	commonHeld, statErr := common.Stat()
	if statErr != nil || !os.SameFile(commonBefore, commonHeld) {
		result.close()
		return managedHooksDirectory{}, fmt.Errorf("git common directory path changed while managing hooks: %s", commonPath)
	}
	if err := result.validate(); err != nil {
		result.close()
		return managedHooksDirectory{}, err
	}
	commonFD := int(common.Fd())
	if err := unix.Mkdirat(commonFD, filepath.Base(managed), 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
		result.close()
		return managedHooksDirectory{}, fmt.Errorf("create managed hooks directory %s: %w", managed, err)
	}
	if err := validateManagedHooksDirectory(managed); err != nil {
		result.close()
		return managedHooksDirectory{}, err
	}
	directoryFD, err := unix.Openat(commonFD, filepath.Base(managed), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		result.close()
		return managedHooksDirectory{}, fmt.Errorf("open managed hooks directory %s without following links: %w", managed, err)
	}
	result.directory = os.NewFile(uintptr(directoryFD), "wb-hooks-managed")
	if result.directory == nil {
		_ = unix.Close(directoryFD)
		result.close()
		return managedHooksDirectory{}, fmt.Errorf("wrap managed hooks directory %s", managed)
	}
	if err := result.validate(); err != nil {
		result.close()
		return managedHooksDirectory{}, err
	}
	return result, nil
}

func (managed managedHooksDirectory) close() {
	if managed.directory != nil {
		_ = managed.directory.Close()
	}
	if managed.common != nil {
		_ = managed.common.Close()
	}
	if managed.repo != nil {
		_ = managed.repo.Close()
	}
}

func (managed managedHooksDirectory) validate() error {
	if managed.repo != nil && !managedDirectoryPathMatches(managed.repoPath, managed.repo) {
		return fmt.Errorf("repository directory path changed while managing hooks: %s", managed.repoPath)
	}
	if !managedDirectoryPathMatches(managed.commonPath, managed.common) {
		return fmt.Errorf("git common directory path changed while managing hooks: %s", managed.commonPath)
	}
	if managed.directory != nil && !managedDirectoryPathMatches(managed.path, managed.directory) {
		return fmt.Errorf("managed hooks directory path changed while managing hooks: %s", managed.path)
	}
	return nil
}

func managedDirectoryPathMatches(path string, directory *os.File) bool {
	if directory == nil {
		return false
	}
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() {
		return false
	}
	held, err := directory.Stat()
	return err == nil && os.SameFile(current, held)
}

func inspectHooksDirectoryPath(path, description string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect %s %s: %w", description, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing symlinked %s %s", description, path)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory: %s", description, path)
	}
	return info, nil
}

func openAbsoluteHooksDirectoryNoFollow(path string) (*os.File, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("secure hooks directory path must be absolute: %s", path)
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open filesystem root: %w", err)
	}
	if path == string(filepath.Separator) {
		directory := os.NewFile(uintptr(fd), "wb-hooks-directory")
		if directory == nil {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("wrap secure hooks directory %s", path)
		}
		return directory, nil
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if segment == "" || segment == "." || segment == ".." {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("invalid secure hooks directory segment %q", segment)
		}
		next, openErr := unix.Openat(fd, segment, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	directory := os.NewFile(uintptr(fd), "wb-hooks-directory")
	if directory == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap secure hooks directory %s", path)
	}
	return directory, nil
}

func validateManagedHooksDirectory(managed string) error {
	info, err := os.Lstat(managed)
	if err != nil {
		return fmt.Errorf("inspect managed hooks directory %s: %w", managed, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlinked managed hooks directory %s", managed)
	}
	if !info.IsDir() {
		return fmt.Errorf("managed hooks path is not a directory: %s", managed)
	}
	return nil
}

func writeExecutable(path string, content []byte) error {
	directory, err := openManagedHooksDirectory("", filepath.Dir(path), nil)
	if err != nil {
		return err
	}
	defer directory.close()
	identity, err := managedHookIdentityAt(directory.directory, filepath.Base(path))
	if err != nil {
		return err
	}
	return writeExecutableAt(directory, filepath.Base(path), content, identity, nil)
}

func managedHookIdentityAt(directory *os.File, name string) (managedHookIdentity, error) {
	if filepath.Base(name) != name || name == "." || name == "" {
		return managedHookIdentity{}, fmt.Errorf("invalid managed hook name %q", name)
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return absentManagedHookIdentity(), nil
		}
		return managedHookIdentity{}, err
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return managedHookIdentity{}, fmt.Errorf("refusing symlinked managed hook %s", name)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return managedHookIdentity{}, fmt.Errorf("managed hook %s is not a regular file", name)
	}
	return managedHookIdentity{exists: true, device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

func verifyManagedHookIdentity(managed managedHooksDirectory, name string, expected managedHookIdentity) error {
	if err := managed.validate(); err != nil {
		return err
	}
	actual, err := managedHookIdentityAt(managed.directory, name)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("managed hook %s changed after inspection; refusing mutation", name)
	}
	return nil
}

// managedHookQuarantineName generates an ignored, collision-resistant name
// for a verified hook that must be moved out of the active hook namespace.
// The subsequent rename is still no-replace: randomness avoids accidental
// collisions while the syscall supplies the security guarantee.
func managedHookQuarantineName(name string) (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generate managed hook quarantine name for %s: %w", name, err)
	}
	return fmt.Sprintf("%s.wb-backup-%x", name, token[:]), nil
}

// moveExpectedManagedHookNoReplace quarantines one inspected hook without ever
// replacing its destination. It checks the moved inode as well as the source:
// if an actor substituted the source between validation and rename, WB restores
// that substituted file with a no-clobber rename and refuses the mutation.
func moveExpectedManagedHookNoReplace(managed managedHooksDirectory, name, destination string, expected managedHookIdentity, afterAuthorization func(name string)) error {
	if !expected.exists {
		return fmt.Errorf("cannot quarantine absent managed hook %s", name)
	}
	if err := verifyManagedHookIdentity(managed, name, expected); err != nil {
		return err
	}
	if err := verifyManagedHookIdentity(managed, destination, absentManagedHookIdentity()); err != nil {
		return fmt.Errorf("verify managed hook quarantine destination %s: %w", destination, err)
	}
	if afterAuthorization != nil {
		afterAuthorization(name)
	}
	if err := renameNoReplace(int(managed.directory.Fd()), name, int(managed.directory.Fd()), destination); err != nil {
		return err
	}
	actual, err := managedHookIdentityAt(managed.directory, destination)
	if err != nil {
		return fmt.Errorf("inspect quarantined managed hook %s: %w", name, err)
	}
	if actual == expected {
		return nil
	}
	if restoreErr := renameNoReplace(int(managed.directory.Fd()), destination, int(managed.directory.Fd()), name); restoreErr != nil {
		return fmt.Errorf("managed hook %s changed after inspection; preserve substituted hook: %v", name, restoreErr)
	}
	return fmt.Errorf("managed hook %s changed after inspection; refusing mutation", name)
}

// quarantineManagedHook moves an existing expected hook aside before an
// activation. The original remains as a `.wb-backup-*` artifact rather than
// being unlinked by pathname later, so a post-validation replacement cannot be
// deleted by cleanup or activation.
func quarantineManagedHook(managed managedHooksDirectory, name string, expected managedHookIdentity, afterAuthorization func(name string)) (string, error) {
	if !expected.exists {
		if err := verifyManagedHookIdentity(managed, name, expected); err != nil {
			return "", err
		}
		if afterAuthorization != nil {
			afterAuthorization(name)
		}
		return "", nil
	}
	quarantineName, err := managedHookQuarantineName(name)
	if err != nil {
		return "", err
	}
	if err := moveExpectedManagedHookNoReplace(managed, name, quarantineName, expected, afterAuthorization); err != nil {
		return "", err
	}
	return quarantineName, nil
}

func readManagedHook(directory *os.File, name string) (managedHookSnapshot, error) {
	if filepath.Base(name) != name || name == "." || name == "" {
		return managedHookSnapshot{}, fmt.Errorf("invalid managed hook name %q", name)
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return managedHookSnapshot{}, err
	}
	file := os.NewFile(uintptr(fd), "wb-managed-hook")
	if file == nil {
		_ = unix.Close(fd)
		return managedHookSnapshot{}, fmt.Errorf("wrap managed hook %s", name)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return managedHookSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return managedHookSnapshot{}, fmt.Errorf("managed hook %s is not a regular file", name)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return managedHookSnapshot{}, fmt.Errorf("inspect managed hook identity %s: %w", name, err)
	}
	content, err := io.ReadAll(file)
	if err != nil {
		return managedHookSnapshot{}, err
	}
	return managedHookSnapshot{content: content, identity: managedHookIdentity{exists: true, device: uint64(stat.Dev), inode: uint64(stat.Ino)}, mode: info.Mode()}, nil
}

func writeExecutableAt(managed managedHooksDirectory, name string, content []byte, expected managedHookIdentity, afterAuthorization func(name string)) error {
	if filepath.Base(name) != name || name == "." || name == "" {
		return fmt.Errorf("invalid managed hook name %q", name)
	}
	var temporaryName string
	var file *os.File
	for attempt := 0; attempt < 16; attempt++ {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return fmt.Errorf("generate temporary hook name for %s: %w", name, err)
		}
		temporaryName = fmt.Sprintf(".%s.wb-tmp-%x", name, token[:])
		fd, err := unix.Openat(int(managed.directory.Fd()), temporaryName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW, 0o755)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return fmt.Errorf("create temporary hook for %s: %w", name, err)
		}
		file = os.NewFile(uintptr(fd), "wb-managed-hook-temp")
		if file == nil {
			_ = unix.Close(fd)
			return fmt.Errorf("wrap temporary hook for %s", name)
		}
		break
	}
	if file == nil {
		return fmt.Errorf("create collision-free temporary hook for %s", name)
	}
	var temporaryStat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &temporaryStat); err != nil {
		_ = file.Close()
		return fmt.Errorf("inspect temporary hook %s: %w", name, err)
	}
	temporaryIdentity := managedHookIdentity{exists: true, device: uint64(temporaryStat.Dev), inode: uint64(temporaryStat.Ino)}
	temporaryPublished := false
	defer func() {
		if !temporaryPublished {
			// Never unlink a random temporary pathname: an actor that observed it
			// could replace it after an identity check. Quarantine WB's inode (or
			// preserve the replacement) with the same no-clobber protocol used for
			// active hooks.
			_, _ = quarantineManagedHook(managed, temporaryName, temporaryIdentity, nil)
		}
	}()
	if err := unix.Fchmod(int(file.Fd()), 0o755); err != nil {
		_ = file.Close()
		return fmt.Errorf("chmod temporary hook %s: %w", name, err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return fmt.Errorf("write hook %s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary hook %s: %w", name, err)
	}
	parkedName, err := quarantineManagedHook(managed, name, expected, afterAuthorization)
	if err != nil {
		return err
	}
	if err := renameNoReplace(int(managed.directory.Fd()), temporaryName, int(managed.directory.Fd()), name); err != nil {
		if parkedName != "" {
			if restoreErr := renameNoReplace(int(managed.directory.Fd()), parkedName, int(managed.directory.Fd()), name); restoreErr != nil {
				return fmt.Errorf("activate hook %s: %w; preserve quarantined hook %s: %v", name, err, parkedName, restoreErr)
			}
		}
		return fmt.Errorf("activate hook %s: %w", name, err)
	}
	temporaryPublished = true
	return nil
}

func backupManagedHook(managed managedHooksDirectory, name, backupName string, expected managedHookIdentity, afterAuthorization func(name string)) error {
	if err := moveExpectedManagedHookNoReplace(managed, name, backupName, expected, afterAuthorization); err != nil {
		return fmt.Errorf("back up managed hook %s: %w", name, err)
	}
	return nil
}

func removeStaleManagedHooksAt(managed managedHooksDirectory, expectedNames []string, actions *[]string, afterRead func(name string), afterAuthorization func(name string)) error {
	expected := map[string]bool{}
	for _, name := range expectedNames {
		expected[name] = true
	}
	if err := managed.validate(); err != nil {
		return err
	}
	if _, err := managed.directory.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind managed hooks directory: %w", err)
	}
	entries, err := managed.directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || expected[entry.Name()] || strings.Contains(entry.Name(), ".wb-backup-") {
			continue
		}
		snapshot, readErr := readManagedHook(managed.directory, entry.Name())
		if readErr != nil || !isManagedContent(string(snapshot.content)) {
			continue
		}
		if afterRead != nil {
			afterRead(entry.Name())
		}
		withoutManaged, err := removeManagedSection(string(snapshot.content))
		if err != nil {
			return fmt.Errorf("remove stale managed section from %s: %w", entry.Name(), err)
		}
		if hasUserHookContent(withoutManaged) {
			if err := writeExecutableAt(managed, entry.Name(), []byte(withoutManaged), snapshot.identity, afterAuthorization); err != nil {
				return err
			}
			*actions = append(*actions, "removed stale WB section from "+entry.Name()+" and preserved user commands")
			continue
		}
		backupName, err := managedHookQuarantineName(entry.Name())
		if err != nil {
			return err
		}
		if err := moveExpectedManagedHookNoReplace(managed, entry.Name(), backupName, snapshot.identity, afterAuthorization); err != nil {
			return fmt.Errorf("quarantine stale managed hook %s: %w", entry.Name(), err)
		}
		*actions = append(*actions, "quarantined stale managed hook "+entry.Name())
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

// repositoryHeadCommitTime reads HEAD's own commit time, the only staleness
// baseline available without relying on Go's automatic VCS build stamping
// (which silently omits revision info when building from a linked worktree —
// exactly how a dogfooded, branch-specific wb build is made).
func repositoryHeadCommitTime(repoRoot string) (time.Time, error) {
	value, err := gitOutput(repoRoot, "log", "-1", "--format=%cI")
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, value)
}

// wbSourceModulePath is wb's own Go module path.
const wbSourceModulePath = "github.com/sneat-dev/wb"

// repositoryIsWBSourceModule reports whether repoRoot is wb's own source
// checkout, the only repository where comparing a wb build's mtime against
// HEAD's commit time is meaningful.
func repositoryIsWBSourceModule(repoRoot string) bool {
	data, err := os.ReadFile(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if field, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(field) == wbSourceModulePath
		}
	}
	return false
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
