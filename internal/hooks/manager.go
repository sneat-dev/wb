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
	// The shim intentionally retains a stable launcher (for example
	// /opt/homebrew/bin/wb) instead of the version-specific target of that
	// launcher symlink. Normalise only its spelling here so `hooks check`
	// compares the same durable entry point that Apply recorded.
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
				if err := backupManagedHook(managedDirectory, name, backupName, existing.identity); err != nil {
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
			}
		} else if !os.IsNotExist(readErr) {
			return ApplyResult{}, fmt.Errorf("read managed hook %s: %w", filepath.Join(managed, name), readErr)
		}
		if err := writeExecutableAt(managedDirectory, name, []byte(content), expectedIdentity); err != nil {
			return ApplyResult{}, err
		}
		result.Actions = append(result.Actions, "installed "+name)
	}
	if options.Repair {
		if err := managedDirectory.validate(); err != nil {
			return ApplyResult{}, err
		}
		if err := removeStaleManagedHooksAt(managedDirectory, names, &result.Actions, options.afterManagedHookRead); err != nil {
			return ApplyResult{}, err
		}
	}
	if current != managed {
		if err := setHooksPath(policy.RepoRoot, managed); err != nil {
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
	return writeExecutableAt(directory, filepath.Base(path), content, identity)
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
	return managedHookSnapshot{content: content, identity: managedHookIdentity{exists: true, device: uint64(stat.Dev), inode: uint64(stat.Ino)}}, nil
}

func writeExecutableAt(managed managedHooksDirectory, name string, content []byte, expected managedHookIdentity) error {
	if filepath.Base(name) != name || name == "." || name == "" {
		return fmt.Errorf("invalid managed hook name %q", name)
	}
	if err := verifyManagedHookIdentity(managed, name, expected); err != nil {
		return err
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
	defer func() { _ = unix.Unlinkat(int(managed.directory.Fd()), temporaryName, 0) }()
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
	if err := verifyManagedHookIdentity(managed, name, expected); err != nil {
		return err
	}
	if err := unix.Renameat(int(managed.directory.Fd()), temporaryName, int(managed.directory.Fd()), name); err != nil {
		return fmt.Errorf("activate hook %s: %w", name, err)
	}
	return nil
}

func backupManagedHook(managed managedHooksDirectory, name, backupName string, expected managedHookIdentity) error {
	if err := verifyManagedHookIdentity(managed, name, expected); err != nil {
		return err
	}
	if err := verifyManagedHookIdentity(managed, backupName, absentManagedHookIdentity()); err != nil {
		return fmt.Errorf("verify managed hook backup destination %s: %w", backupName, err)
	}
	if err := unix.Renameat(int(managed.directory.Fd()), name, int(managed.directory.Fd()), backupName); err != nil {
		return fmt.Errorf("back up managed hook %s: %w", name, err)
	}
	return nil
}

func removeStaleManagedHooksAt(managed managedHooksDirectory, expectedNames []string, actions *[]string, afterRead func(name string)) error {
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
			if err := writeExecutableAt(managed, entry.Name(), []byte(withoutManaged), snapshot.identity); err != nil {
				return err
			}
			*actions = append(*actions, "removed stale WB section from "+entry.Name()+" and preserved user commands")
			continue
		}
		if err := verifyManagedHookIdentity(managed, entry.Name(), snapshot.identity); err != nil {
			return err
		}
		if err := unix.Unlinkat(int(managed.directory.Fd()), entry.Name(), 0); err != nil {
			return fmt.Errorf("remove stale managed hook %s: %w", entry.Name(), err)
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
