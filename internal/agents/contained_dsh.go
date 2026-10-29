package agents

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// This file owns the bounded preparation of one contained dsh launch. It writes
// exactly one private input directory below the run, never the whole run store
// and never the host harness home. The contained owner consumes it only after
// the fresh configuration has re-admitted the record's mandatory policy.
//
// The launch carries only public launch fields and a resolved credential
// *name*. There is deliberately no field for a credential value: the value
// stays in the owner's process environment and is injected by the transport,
// never written to the overlay, an argument or a record.

const (
	// containedDshInputDirName is the only directory this helper creates. It is
	// fixed so a caller cannot widen the mount or reuse another run's inputs.
	containedDshInputDirName = "container-input"
	// containedDshPatchTarget is the fixed in-container path of the overlay.
	containedDshPatchTarget = containerInputTarget + "/" + dshModelPatchName
	// containedDshDefaultBaseURL is the official public DeepSeek API base used
	// only when an explicit launch field names no routing base.
	containedDshDefaultBaseURL = "https://api.deepseek.com"
	// containedDshReasoningEffort, containedDshThinking and containedDshMaxTokens
	// are the public bounded Flash defaults the approved profile pins. They are
	// constants so no caller can raise an output bound or request a different
	// reasoning mode through this helper.
	containedDshReasoningEffort = "low"
	containedDshThinking        = "enabled"
	containedDshMaxTokens       = 32768
)

// ContainedDshLaunchFields are the exact public inputs to one contained dsh
// preparation. Provider and Harness are exact names, not aliases; Model is
// passed through verbatim. CredentialEnvName is a variable *name*; the value is
// never an input here.
type ContainedDshLaunchFields struct {
	RunDir            string
	WorktreeDir       string
	GitCommonDir      string
	Provider          string
	Harness           string
	Model             string
	Task              string
	BaseURL           string
	CredentialEnvName string
}

// ContainedDshLaunch is one prepared contained launch: the exact trusted
// workspace (with the newly generated scoped input directory) and the harness
// invocation whose patch path is the fixed in-container target.
type ContainedDshLaunch struct {
	Workspace *ContainerWorkspace
	Launch    HarnessLaunch
}

// containedDshPatchEntry is one compose-tree overlay entry. Only the fields a
// contained dsh launch pins are representable, so an arbitrary runtime option
// cannot travel through a patch.
type containedDshPatchEntry struct {
	ID     string                  `yaml:"id"`
	Config containedDshPatchConfig `yaml:"config"`
}

type containedDshPatchConfig struct {
	Provider        string `yaml:"provider,omitempty"`
	Model           string `yaml:"model,omitempty"`
	APIKeyEnv       string `yaml:"apiKeyEnv,omitempty"`
	BaseURL         string `yaml:"baseURL,omitempty"`
	ReasoningEffort string `yaml:"reasoningEffort,omitempty"`
	Thinking        string `yaml:"thinking,omitempty"`
	MaxTokens       int    `yaml:"maxTokens,omitempty"`
}

// containedDshModelPatch renders the overlay that pins the exact model, the
// official provider label, the injected credential variable *name* and the
// official routing base, plus the bounded Flash defaults. The credential value
// is not an input to this function.
func containedDshModelPatch(model, credentialEnvName, baseURL string) []byte {
	entries := []containedDshPatchEntry{
		{
			ID: "agent-default-model",
			Config: containedDshPatchConfig{
				Provider: ProviderDshDefault,
				Model:    model,
			},
		},
		{
			ID: "llm-deepseek",
			Config: containedDshPatchConfig{
				APIKeyEnv:       credentialEnvName,
				BaseURL:         baseURL,
				ReasoningEffort: containedDshReasoningEffort,
				Thinking:        containedDshThinking,
				MaxTokens:       containedDshMaxTokens,
			},
		},
	}
	raw, _ := yaml.Marshal(entries)
	return raw
}

// PrepareContainedDshLaunchFromRecord prepares a launch from an admitted run
// record. It trusts only the record's snapped workspace, refuses any input
// directory the record tries to supply, and requires the resolved credential
// name to match the record's own routing.
func PrepareContainedDshLaunchFromRecord(record Record, runDir, credentialEnvName string) (ContainedDshLaunch, error) {
	workspace := record.ExecutionWorkspace
	if workspace == nil {
		return ContainedDshLaunch{}, fmt.Errorf("prepare a contained dsh launch: the record carries no trusted execution workspace")
	}
	if workspace.InputDir != "" {
		return ContainedDshLaunch{}, fmt.Errorf("prepare a contained dsh launch: the record supplies input directory %q; the scoped input must be generated below this run", workspace.InputDir)
	}
	if strings.TrimSpace(record.WorktreeDir) == "" {
		return ContainedDshLaunch{}, fmt.Errorf("prepare a contained dsh launch: the record carries no trusted worktree directory")
	}
	if workspace.WorktreeDir != record.WorktreeDir {
		return ContainedDshLaunch{}, fmt.Errorf("prepare a contained dsh launch: the trusted workspace worktree %q does not match the record worktree %q", workspace.WorktreeDir, record.WorktreeDir)
	}
	if err := containedDshCredentialMatchesRouting(record.Resolved.Routing, credentialEnvName); err != nil {
		return ContainedDshLaunch{}, err
	}
	return PrepareContainedDshLaunch(ContainedDshLaunchFields{
		RunDir:            runDir,
		WorktreeDir:       workspace.WorktreeDir,
		GitCommonDir:      workspace.GitCommonDir,
		Provider:          record.Resolved.Provider,
		Harness:           record.Resolved.Harness,
		Model:             record.Resolved.Model,
		Task:              record.Task,
		BaseURL:           record.Resolved.Routing.BaseURL,
		CredentialEnvName: credentialEnvName,
	})
}

// containedDshFile is the opened overlay file surface the preparation needs.
// Only the two operations the write path uses are representable, so the seam
// cannot be widened into a general file abstraction.
type containedDshFile interface {
	Write([]byte) (int, error)
	Close() error
}

// containedDshFilesystem is the minimal per-call filesystem surface the scoped
// input preparation uses. Production supplies the exact os operations; a
// focused test supplies a failure through one call without replacing any
// process-wide function or exposing a public option.
type containedDshFilesystem struct {
	mkdir        func(string, os.FileMode) error
	removeAll    func(string) error
	lstat        func(string) (os.FileInfo, error)
	evalSymlinks func(string) (string, error)
	openFile     func(string, int, os.FileMode) (containedDshFile, error)
}

func defaultContainedDshFilesystem() containedDshFilesystem {
	return containedDshFilesystem{
		mkdir:        os.Mkdir,
		removeAll:    os.RemoveAll,
		lstat:        os.Lstat,
		evalSymlinks: filepath.EvalSymlinks,
		openFile: func(path string, flag int, perm os.FileMode) (containedDshFile, error) {
			return os.OpenFile(path, flag, perm)
		},
	}
}

// PrepareContainedDshLaunch writes the exact scoped input directory and returns
// the workspace and harness launch. It creates no other file or mount, so a
// caller cannot add an input file, reuse the run store or point the harness at a
// host harness home.
func PrepareContainedDshLaunch(fields ContainedDshLaunchFields) (ContainedDshLaunch, error) {
	return prepareContainedDshLaunch(fields, defaultContainedDshFilesystem())
}

func prepareContainedDshLaunch(fields ContainedDshLaunchFields, filesystem containedDshFilesystem) (ContainedDshLaunch, error) {
	if fields.Provider != ProviderDshDefault {
		return ContainedDshLaunch{}, fmt.Errorf("prepare a contained dsh launch: provider %q is not the only official provider %q", fields.Provider, ProviderDshDefault)
	}
	if fields.Harness != HarnessDSH {
		return ContainedDshLaunch{}, fmt.Errorf("prepare a contained dsh launch: harness %q is not %q", fields.Harness, HarnessDSH)
	}
	model := fields.Model
	if model == "" || model != strings.TrimSpace(model) {
		return ContainedDshLaunch{}, fmt.Errorf("prepare a contained dsh launch: model %q must be a non-empty exact identifier", fields.Model)
	}
	if err := validateExecutionValue("model", model, true); err != nil {
		return ContainedDshLaunch{}, fmt.Errorf("prepare a contained dsh launch: %w", err)
	}
	credentialEnvName, err := validateContainedDshCredentialName(fields.CredentialEnvName)
	if err != nil {
		return ContainedDshLaunch{}, err
	}
	baseURL := fields.BaseURL
	if baseURL == "" {
		baseURL = containedDshDefaultBaseURL
	}
	if !officialDeepSeekEndpoint(baseURL) {
		return ContainedDshLaunch{}, fmt.Errorf("prepare a contained dsh launch: base URL %q is not the official HTTPS DeepSeek endpoint", fields.BaseURL)
	}
	runDir, err := validateContainedDshRunDirWith(filesystem, fields.RunDir)
	if err != nil {
		return ContainedDshLaunch{}, err
	}
	worktree, err := validateContainedDshPath("worktree directory", fields.WorktreeDir)
	if err != nil {
		return ContainedDshLaunch{}, err
	}
	gitCommon, err := validateContainedDshPath("git common directory", fields.GitCommonDir)
	if err != nil {
		return ContainedDshLaunch{}, err
	}
	if containerPathsOverlap(worktree, gitCommon) {
		return ContainedDshLaunch{}, fmt.Errorf("prepare a contained dsh launch: worktree %q and git common directory %q must be disjoint", worktree, gitCommon)
	}
	inputDir := filepath.Join(runDir, containedDshInputDirName)
	if containerPathsOverlap(inputDir, worktree) || containerPathsOverlap(inputDir, gitCommon) {
		return ContainedDshLaunch{}, fmt.Errorf("prepare a contained dsh launch: scoped input directory %q overlaps the worktree or git common directory", inputDir)
	}
	taskArgument, err := dshTaskArgument(fields.Task)
	if err != nil {
		return ContainedDshLaunch{}, fmt.Errorf("prepare a contained dsh launch: %w", err)
	}
	if err := validateContainedDshInputTargetWith(filesystem, inputDir); err != nil {
		return ContainedDshLaunch{}, err
	}
	if err := filesystem.mkdir(inputDir, 0o700); err != nil {
		return ContainedDshLaunch{}, fmt.Errorf("prepare a contained dsh launch: create scoped input directory %s: %w", inputDir, err)
	}
	// From here on every failure removes the one directory this call created.
	cleanup := func() { _ = filesystem.removeAll(inputDir) }
	patchPath := filepath.Join(inputDir, dshModelPatchName)
	if err := writeContainedDshPatchWith(filesystem, patchPath, containedDshModelPatch(model, credentialEnvName, baseURL)); err != nil {
		cleanup()
		return ContainedDshLaunch{}, err
	}
	workspace := &ContainerWorkspace{WorktreeDir: worktree, GitCommonDir: gitCommon, InputDir: inputDir}
	launch := HarnessLaunch{
		Argv: []string{
			"--profile", dshProfile,
			"--patch", containedDshPatchTarget,
			taskArgument,
		},
		// PrivateFiles names the real host file so a reader can find it; the
		// harness argv is the only place the fixed container path appears.
		PrivateFiles: []string{patchPath},
		SplitStderr:  true,
	}
	return ContainedDshLaunch{Workspace: workspace, Launch: launch}, nil
}

// containedDshCredentialMatchesRouting requires the resolved credential name to
// be the one the record's own routing would produce. A file-sourced routing is
// injected under WB's fixed provider credential name, exactly as ResolveCredential
// does, so it is compared against that name rather than an empty value.
func containedDshCredentialMatchesRouting(routing Provider, credentialEnvName string) error {
	name, err := validateContainedDshCredentialName(credentialEnvName)
	if err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(routing.CredentialEnv) != "":
		if name != strings.TrimSpace(routing.CredentialEnv) {
			return fmt.Errorf("prepare a contained dsh launch: credential environment name %q does not match the resolved routing name %q", name, routing.CredentialEnv)
		}
	case strings.TrimSpace(routing.CredentialFile) != "":
		if name != ProviderCredentialEnv {
			return fmt.Errorf("prepare a contained dsh launch: a file-sourced credential must be injected as %q, got %q", ProviderCredentialEnv, name)
		}
	default:
		return fmt.Errorf("prepare a contained dsh launch: the resolved routing names no credential source")
	}
	return nil
}

func validateContainedDshCredentialName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("prepare a contained dsh launch: the resolved credential environment name is required")
	}
	if name != raw {
		return "", fmt.Errorf("prepare a contained dsh launch: credential environment name %q has surrounding whitespace", raw)
	}
	if !validContainerCredentialName(name) {
		return "", fmt.Errorf("prepare a contained dsh launch: credential environment name %q is not an uppercase shell variable", name)
	}
	if reservedContainerWorkerEnvName(name) {
		return "", fmt.Errorf("prepare a contained dsh launch: credential environment name %q is reserved for the container transport", name)
	}
	return name, nil
}

// validateContainedDshRunDirWith validates the one existing private run directory
// this helper writes below. A symlink is refused rather than followed, because
// following it would place the scoped input outside the run.
func validateContainedDshRunDirWith(filesystem containedDshFilesystem, raw string) (string, error) {
	runDir, err := validateContainedDshPath("run directory", raw)
	if err != nil {
		return "", err
	}
	info, err := filesystem.lstat(runDir)
	if err != nil {
		return "", fmt.Errorf("prepare a contained dsh launch: inspect run directory %q: %w", runDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("prepare a contained dsh launch: run directory %q is a symlink; refusing to follow it", runDir)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("prepare a contained dsh launch: run directory %q is not a directory", runDir)
	}
	resolved, err := filesystem.evalSymlinks(runDir)
	if err != nil {
		return "", fmt.Errorf("prepare a contained dsh launch: resolve run directory %q: %w", runDir, err)
	}
	if resolved != runDir {
		return "", fmt.Errorf("prepare a contained dsh launch: run directory %q contains a symlink; it resolves to %q", runDir, resolved)
	}
	return runDir, nil
}

func validateContainedDshPath(label, raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", fmt.Errorf("prepare a contained dsh launch: the %s is empty", label)
	}
	if path != raw {
		return "", fmt.Errorf("prepare a contained dsh launch: the %s %q has leading or trailing whitespace", label, raw)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("prepare a contained dsh launch: the %s %q is not absolute", label, path)
	}
	clean := filepath.Clean(path)
	if clean != path {
		return "", fmt.Errorf("prepare a contained dsh launch: the %s %q is not a clean path; use %q", label, path, clean)
	}
	if err := validateContainerMountPath(label, path); err != nil {
		return "", fmt.Errorf("prepare a contained dsh launch: %w", err)
	}
	return clean, nil
}

// validateContainedDshInputTargetWith refuses a scoped input path that already
// exists, including a symlink. Reusing a pre-existing directory would let
// something outside this preparation choose what the container mounts.
func validateContainedDshInputTargetWith(filesystem containedDshFilesystem, inputDir string) error {
	info, err := filesystem.lstat(inputDir)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("prepare a contained dsh launch: scoped input directory %q is a symlink; refusing to follow it", inputDir)
		}
		return fmt.Errorf("prepare a contained dsh launch: scoped input directory %q already exists; refusing to reuse it", inputDir)
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("prepare a contained dsh launch: inspect scoped input directory %q: %w", inputDir, err)
	}
}

// writeContainedDshPatch creates the overlay exclusively with mode 0600. The
// caller owns the fresh directory, so an existing path is an error rather than
// something to truncate.
func writeContainedDshPatch(path string, raw []byte) error {
	return writeContainedDshPatchWith(defaultContainedDshFilesystem(), path, raw)
}

func writeContainedDshPatchWith(filesystem containedDshFilesystem, path string, raw []byte) error {
	file, err := filesystem.openFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("prepare a contained dsh launch: create overlay %s: %w", path, err)
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return fmt.Errorf("prepare a contained dsh launch: write overlay %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("prepare a contained dsh launch: close overlay %s: %w", path, err)
	}
	return nil
}
