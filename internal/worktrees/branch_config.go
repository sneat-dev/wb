package worktrees

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	branchConfigVersion = 1
	maxBranchConfigSize = 64 << 10
)

// branchConfigFile is layered user then repository. A nil prefix leaves the
// lower layer intact; an explicitly empty value deliberately disables it.
type branchConfigFile struct {
	Version   int `yaml:"version"`
	Worktrees struct {
		BranchPrefix *string `yaml:"branch_prefix"`
	} `yaml:"worktrees"`
}

// branchNamingOptions carries only precedence inputs. Agent provenance is
// deliberately absent: Work Logs, not branch spelling, own that data.
type branchNamingOptions struct {
	Task              string
	ExactBranch       string
	ExactBranchChosen bool
	CLIPrefix         string
	CLIPrefixChosen   bool
	Canonical         *canonicalRepository
	BaseRevision      string
	Base              string
}

func deriveBranchName(ctx context.Context, options branchNamingOptions) (string, error) {
	if options.ExactBranchChosen && options.CLIPrefixChosen {
		return "", fmt.Errorf("--branch and --branch-prefix cannot be used together")
	}
	if options.ExactBranchChosen {
		if options.ExactBranch == "" {
			return "", fmt.Errorf("--branch must not be empty when explicitly provided")
		}
		return validateDerivedBranch(options.ExactBranch, options.Base)
	}
	prefix := ""
	if options.CLIPrefixChosen {
		prefix = options.CLIPrefix
		if prefix != "" && !strings.HasSuffix(prefix, "/") {
			return "", fmt.Errorf("branch prefix %q must end with /", prefix)
		}
		if prefix != "" && !validBranch(context.Background(), prefix+"probe") {
			return "", fmt.Errorf("invalid branch prefix %q", prefix)
		}
	} else {
		configured, err := configuredBranchPrefix(ctx, options.Canonical, options.BaseRevision)
		if err != nil {
			return "", err
		}
		prefix = configured
	}
	return validateDerivedBranch(prefix+options.Task, options.Base)
}

func validateDerivedBranch(branch, base string) (string, error) {
	branch = strings.TrimSpace(branch)
	if !validBranch(context.Background(), branch) {
		return "", fmt.Errorf("invalid feature branch %q", branch)
	}
	if branch == base {
		return "", fmt.Errorf("feature branch must differ from base branch %q", base)
	}
	return branch, nil
}

// configuredBranchPrefix reads the repository layer from the exact fetched
// target-base object. A clean canonical checkout may intentionally be on a
// different branch, so its filesystem is never policy authority here.
func configuredBranchPrefix(ctx context.Context, canonical *canonicalRepository, baseRevision string) (string, error) {
	globalPath, err := defaultWorktreesConfigPath()
	if err != nil {
		return "", err
	}
	prefix := ""
	if config, found, err := loadBranchConfigFile(globalPath); err != nil {
		return "", err
	} else if found && config.Worktrees.BranchPrefix != nil {
		prefix = *config.Worktrees.BranchPrefix
	}
	if canonical == nil || !isGitObjectID(baseRevision) {
		return "", fmt.Errorf("branch policy requires a fetched canonical target-base revision")
	}
	contents, found, err := repositoryBranchConfigAt(ctx, canonical, baseRevision)
	if err != nil {
		return "", err
	}
	if !found {
		return prefix, nil
	}
	config, _, err := parseBranchConfig(".wb/worktrees.yaml at "+baseRevision, contents)
	if err != nil {
		return "", err
	}
	if config.Worktrees.BranchPrefix != nil {
		prefix = *config.Worktrees.BranchPrefix
	}
	return prefix, nil
}

func defaultWorktreesConfigPath() (string, error) {
	if configHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); configHome != "" {
		return filepath.Join(configHome, "wb", "worktrees.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "wb", "worktrees.yaml"), nil
}

func loadBranchConfigFile(path string) (branchConfigFile, bool, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		return branchConfigFile{}, false, nil
	}
	if err != nil {
		return branchConfigFile{}, false, fmt.Errorf("resolve worktrees config %s: %w", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return branchConfigFile{}, false, fmt.Errorf("inspect worktrees config %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return branchConfigFile{}, false, fmt.Errorf("worktrees config %s must resolve to a regular file", path)
	}
	if info.Size() > maxBranchConfigSize {
		return branchConfigFile{}, false, fmt.Errorf("worktrees config %s exceeds %d-byte limit", path, maxBranchConfigSize)
	}
	contents, err := os.ReadFile(resolved)
	if err != nil {
		return branchConfigFile{}, false, fmt.Errorf("read worktrees config %s: %w", path, err)
	}
	return parseBranchConfig(path, contents)
}

func repositoryBranchConfigAt(ctx context.Context, canonical *canonicalRepository, revision string) ([]byte, bool, error) {
	// ls-tree has a typed empty result for an absent path. Never infer policy
	// absence from Git's human-facing stderr; its entry type also lets us reject
	// symlinks and submodules before bytes influence branch naming.
	// gitCanonical runs from the retained common Git directory. --full-tree
	// makes this exact-tree query independent of that CWD's pathspec prefix.
	entry, err := gitCanonical(ctx, canonical, "ls-tree", "--full-tree", revision, "--", ".wb/worktrees.yaml")
	if err != nil {
		return nil, false, fmt.Errorf("inspect repository worktrees policy at %s: %w", revision, err)
	}
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return nil, false, nil
	}
	metadata, path, found := strings.Cut(entry, "\t")
	fields := strings.Fields(metadata)
	if !found || path != ".wb/worktrees.yaml" || len(fields) != 3 || fields[1] != "blob" ||
		(fields[0] != "100644" && fields[0] != "100755") || !isGitObjectID(fields[2]) {
		return nil, false, fmt.Errorf("repository worktrees policy at %s must be a regular blob, not %q", revision, entry)
	}
	sizeOutput, err := gitCanonical(ctx, canonical, "cat-file", "-s", fields[2])
	if err != nil {
		return nil, false, fmt.Errorf("inspect repository worktrees policy blob size at %s: %w", revision, err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeOutput), 10, 64)
	if err != nil || size < 0 {
		return nil, false, fmt.Errorf("parse repository worktrees policy blob size at %s: %q", revision, strings.TrimSpace(sizeOutput))
	}
	if size > maxBranchConfigSize {
		return nil, false, fmt.Errorf("repository worktrees policy blob at %s exceeds %d-byte limit", revision, maxBranchConfigSize)
	}
	contents, err := gitCanonical(ctx, canonical, "show", fields[2])
	if err != nil {
		return nil, false, fmt.Errorf("read repository worktrees policy blob at %s: %w", revision, err)
	}
	return []byte(contents), true, nil
}

func parseBranchConfig(path string, contents []byte) (branchConfigFile, bool, error) {
	if len(contents) > maxBranchConfigSize {
		return branchConfigFile{}, false, fmt.Errorf("worktrees config %s exceeds %d-byte limit", path, maxBranchConfigSize)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var config branchConfigFile
	if err := decoder.Decode(&config); err != nil {
		return branchConfigFile{}, false, fmt.Errorf("parse worktrees config %s: %w", path, err)
	}
	if config.Version != branchConfigVersion {
		return branchConfigFile{}, false, fmt.Errorf("worktrees config %s has version %d; supported version is %d", path, config.Version, branchConfigVersion)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return branchConfigFile{}, false, fmt.Errorf("parse worktrees config %s: multiple YAML documents are not supported", path)
	} else if !errors.Is(err, io.EOF) {
		return branchConfigFile{}, false, fmt.Errorf("parse worktrees config %s: %w", path, err)
	}
	if config.Worktrees.BranchPrefix != nil {
		prefix := *config.Worktrees.BranchPrefix
		if strings.TrimSpace(prefix) != prefix {
			return branchConfigFile{}, false, fmt.Errorf("worktrees config %s branch_prefix must not have surrounding whitespace", path)
		}
		if prefix != "" && !strings.HasSuffix(prefix, "/") {
			return branchConfigFile{}, false, fmt.Errorf("worktrees config %s branch_prefix must end with /", path)
		}
		if prefix != "" && !validBranch(context.Background(), prefix+"probe") {
			return branchConfigFile{}, false, fmt.Errorf("worktrees config %s has invalid branch_prefix %q", path, prefix)
		}
	}
	return config, true, nil
}
