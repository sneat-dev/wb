// Package worktreepolicy owns the pure worktree configuration format and validation.
// Reading a user or repository policy remains the responsibility of the caller.
package worktreepolicy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/repopath"
	"gopkg.in/yaml.v3"
)

const (
	Version                  = 1
	MaxConfigSize            = 64 << 10
	StoreModeCentral         = "central"
	StoreModeRepositoryLocal = "repository-local"
)

// Config is layered user then repository. A nil prefix leaves the lower layer
// intact; an explicitly empty value deliberately disables it.
type Config struct {
	Version   int `yaml:"version"`
	Worktrees struct {
		BranchPrefix *string `yaml:"branch_prefix"`
		// Store and Root are machine-local user policy, never repository policy.
		Store *string `yaml:"store"`
		Root  *string `yaml:"root"`
	} `yaml:"worktrees"`
	// Retirement is machine-local user policy. A source repository cannot
	// choose where a developer exports private Work Log evidence.
	Retirement struct {
		ArchiveRepository *string                                     `yaml:"archive_repository"`
		Organizations     map[string]RetiredArchiveOrganizationConfig `yaml:"organizations"`
	} `yaml:"retirement"`
}

type RetiredArchiveOrganizationConfig struct {
	ArchiveRepository *string `yaml:"archive_repository"`
}

// BranchValidator checks the complete candidate ref, including the probe
// suffix. The caller supplies its existing Git branch grammar implementation.
type BranchValidator func(string) bool

// Decode validates policy bytes without reading files or invoking Git. The
// found result mirrors the facade contract: true only for a valid document.
func Decode(path string, contents []byte, validBranch BranchValidator) (Config, bool, error) {
	if len(contents) > MaxConfigSize {
		return Config{}, false, fmt.Errorf("worktrees config %s exceeds %d-byte limit", path, MaxConfigSize)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, false, fmt.Errorf("parse worktrees config %s: %w", path, err)
	}
	if config.Version != Version {
		return Config{}, false, fmt.Errorf("worktrees config %s has version %d; supported version is %d", path, config.Version, Version)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return Config{}, false, fmt.Errorf("parse worktrees config %s: multiple YAML documents are not supported", path)
	} else if !errors.Is(err, io.EOF) {
		return Config{}, false, fmt.Errorf("parse worktrees config %s: %w", path, err)
	}
	if config.Worktrees.BranchPrefix != nil {
		prefix := *config.Worktrees.BranchPrefix
		if strings.TrimSpace(prefix) != prefix {
			return Config{}, false, fmt.Errorf("worktrees config %s branch_prefix must not have surrounding whitespace", path)
		}
		if prefix != "" && !strings.HasSuffix(prefix, "/") {
			return Config{}, false, fmt.Errorf("worktrees config %s branch_prefix must end with /", path)
		}
		if prefix != "" && !validBranch(prefix+"probe") {
			return Config{}, false, fmt.Errorf("worktrees config %s has invalid branch_prefix %q", path, prefix)
		}
	}
	if config.Worktrees.Store != nil {
		store := *config.Worktrees.Store
		if strings.TrimSpace(store) != store {
			return Config{}, false, fmt.Errorf("worktrees config %s store must not have surrounding whitespace", path)
		}
		if store != StoreModeCentral && store != StoreModeRepositoryLocal {
			return Config{}, false, fmt.Errorf("worktrees config %s store mode %q is unsupported; use %q or %q", path, store, StoreModeCentral, StoreModeRepositoryLocal)
		}
	}
	if config.Worktrees.Root != nil {
		if strings.TrimSpace(*config.Worktrees.Root) != *config.Worktrees.Root {
			return Config{}, false, fmt.Errorf("worktrees config %s root must not have surrounding whitespace", path)
		}
		if *config.Worktrees.Root == "" {
			return Config{}, false, fmt.Errorf("worktrees config %s root must not be empty", path)
		}
	}
	if config.Retirement.ArchiveRepository != nil {
		if err := ValidateRetiredArchiveRepositoryName(*config.Retirement.ArchiveRepository); err != nil {
			return Config{}, false, fmt.Errorf("worktrees config %s retirement.archive_repository: %w", path, err)
		}
	}
	for organization, policy := range config.Retirement.Organizations {
		if !repopath.SafeSegment(organization, false) {
			return Config{}, false, fmt.Errorf("worktrees config %s retirement.organizations has invalid organization %q", path, organization)
		}
		if policy.ArchiveRepository != nil {
			if err := ValidateRetiredArchiveRepositoryName(*policy.ArchiveRepository); err != nil {
				return Config{}, false, fmt.Errorf("worktrees config %s retirement.organizations.%s.archive_repository: %w", path, organization, err)
			}
		}
	}
	return config, true, nil
}

// ValidateRetiredArchiveRepositoryName preserves the repository-basename
// grammar used by the worktrees facade, including a leading dot.
func ValidateRetiredArchiveRepositoryName(repository string) error {
	if strings.TrimSpace(repository) != repository || repository == "" || !repopath.SafeSegment(repository, true) {
		return fmt.Errorf("must be a non-empty repository basename")
	}
	return nil
}

// RepositoryPlacementPolicy rejects machine-local settings in a tracked policy.
func RepositoryPlacementPolicy(config Config, baseRevision, userConfigPath string) error {
	if config.Worktrees.Store != nil {
		return fmt.Errorf("repository worktrees policy at %s must not set worktrees.store; the store mode is machine-local user policy, set it in %s", baseRevision, userConfigPath)
	}
	if config.Worktrees.Root != nil {
		return fmt.Errorf("repository worktrees policy at %s must not set worktrees.root; the store root is machine-local user policy, set it in %s", baseRevision, userConfigPath)
	}
	if config.Retirement.ArchiveRepository != nil || len(config.Retirement.Organizations) != 0 {
		return fmt.Errorf("repository worktrees policy at %s must not set retirement; retired archive selection is machine-local user policy, set it in %s", baseRevision, userConfigPath)
	}
	return nil
}
