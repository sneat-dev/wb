// Package worktreeclaims owns private worktree manifest, prompt, and claim bindings.
package worktreeclaims

import (
	"errors"
	"fmt"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/worktreelayout"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	journalExcludeRule      = "/.wb/local/"
	manifestName            = "manifest.yaml"
	promptsDirectory        = "prompts"
	promptOrdinalFmt        = "%04d"
	ProvenanceCreated       = "created"
	ProvenanceReconstructed = "reconstructed"
	PromptSourceHarness     = "harness_observed"
	PromptSourceAgent       = "agent_declared"
	PromptSourceHuman       = "human_declared"
	EffortKindFeature       = "feature"
	EffortKindTask          = "task"
)

var ErrManifestNotFound = errors.New("worktree manifest not found")
var promptFileName = regexp.MustCompile(`^([0-9]{4})-[A-Za-z0-9][A-Za-z0-9._-]*\.md$`)

type Manifest struct {
	Version            int       `yaml:"version"`
	EffortID           string    `yaml:"effort_id"`
	ParentEffort       string    `yaml:"parent_effort,omitempty"`
	EffortKind         string    `yaml:"effort_kind"`
	Repository         string    `yaml:"repository"`
	Worktree           string    `yaml:"worktree"`
	Branch             string    `yaml:"branch"`
	Base               string    `yaml:"base"`
	BaseSHA            string    `yaml:"base_sha"`
	CreatedAt          time.Time `yaml:"created_at"`
	Initiator          string    `yaml:"initiator,omitempty"`
	AgentID            string    `yaml:"agent_id,omitempty"`
	AgentRuntime       string    `yaml:"agent_runtime,omitempty"`
	Model              string    `yaml:"model,omitempty"`
	CLI                string    `yaml:"cli,omitempty"`
	Provider           string    `yaml:"provider,omitempty"`
	DependencyCampaign bool      `yaml:"dependency_campaign,omitempty"`
	RunID              string    `yaml:"run_id,omitempty"`
	ClaimID            string    `yaml:"claim_id,omitempty"`
	Provenance         string    `yaml:"provenance"`

	// InferredFields and Evidence are populated only for a reconstructed
	// manifest, so a reader can see exactly which values were guessed and from
	// what. They stay empty for provenance: created.
	InferredFields []string `yaml:"inferred_fields,omitempty"`
	Evidence       []string `yaml:"evidence,omitempty"`
}

type PromptHeader struct {
	Seq      int       `yaml:"seq"`
	At       time.Time `yaml:"at"`
	SHA256   string    `yaml:"sha256"`
	Source   string    `yaml:"source"`
	Runtime  string    `yaml:"runtime,omitempty"`
	Model    string    `yaml:"model,omitempty"`
	CLI      string    `yaml:"cli,omitempty"`
	Provider string    `yaml:"provider,omitempty"`
	Slug     string    `yaml:"-"`
}

func ValidEffortPath(value string) bool {
	if value == "" || len(value) > 200 {
		return false
	}
	if strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, segment := range strings.Split(value, ".") {
		if segment == "" || !worktreelayout.ValidSafeSegment(segment) {
			return false
		}
	}
	return true
}

func ParentEffort(value string) string {
	index := strings.LastIndex(value, ".")
	if index <= 0 {
		return ""
	}
	return value[:index]
}

func EffortKindFor(value string) string {
	if ParentEffort(value) == "" {
		return EffortKindFeature
	}
	return EffortKindTask
}

func IsAncestorEffort(ancestor, descendant string) bool {
	return ancestor != "" && strings.HasPrefix(descendant, ancestor+".")
}

func ValidateManifest(manifest Manifest) error {
	if manifest.Version != 1 {
		return fmt.Errorf("unsupported worktree manifest version %d", manifest.Version)
	}
	if !ValidEffortPath(manifest.EffortID) {
		return fmt.Errorf("invalid manifest effort id %q", manifest.EffortID)
	}
	if manifest.ParentEffort != "" && manifest.ParentEffort != ParentEffort(manifest.EffortID) {
		return fmt.Errorf(
			"manifest parent effort %q contradicts effort path %q; the path is authoritative",
			manifest.ParentEffort, manifest.EffortID,
		)
	}
	switch manifest.EffortKind {
	case EffortKindFeature, EffortKindTask:
	default:
		return fmt.Errorf("invalid manifest effort kind %q", manifest.EffortKind)
	}
	switch manifest.Provenance {
	case ProvenanceCreated:
		if len(manifest.InferredFields) != 0 {
			return fmt.Errorf("a created manifest cannot record inferred fields")
		}
	case ProvenanceReconstructed:
		if len(manifest.InferredFields) == 0 {
			return fmt.Errorf("a reconstructed manifest must record which fields were inferred")
		}
	default:
		return fmt.Errorf("invalid manifest provenance %q", manifest.Provenance)
	}
	if strings.TrimSpace(manifest.Branch) == "" || strings.TrimSpace(manifest.Repository) == "" {
		return fmt.Errorf("manifest must record repository and branch")
	}
	return nil
}

func OwnerAgent(runtime, agentID string) string {
	if runtime = strings.TrimSpace(runtime); runtime != "" {
		return runtime
	}
	return strings.TrimSpace(agentID)
}

func EffortFromWorktreePath(worktree string) string {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(worktree)), "/")
	if len(parts) >= 2 && parts[len(parts)-2] == ".worktrees" {
		candidate := parts[len(parts)-1]
		if ValidEffortPath(candidate) {
			return candidate
		}
		return ""
	}
	if len(parts) < 3 {
		return ""
	}
	candidate := parts[len(parts)-3]
	if !ValidEffortPath(candidate) {
		return ""
	}
	return candidate
}

func RepositoryFromWorktreePath(worktree string) string {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(worktree)), "/")
	if len(parts) >= 2 {
		owner, name := parts[len(parts)-2], parts[len(parts)-1]
		if worktreelayout.ValidSafeSegment(owner) && worktreelayout.ValidRepositorySegment(name) {
			return owner + "/" + name
		}
	}
	if len(parts) >= 1 && worktreelayout.ValidRepositorySegment(parts[len(parts)-1]) {
		return "unknown/" + parts[len(parts)-1]
	}
	return ""
}

type LockOps struct {
	Chmod func(int, uint32) error
	Flock func(int, int) error
}

func LockJournalSequence(directory *os.File, name string) (func(), error) {
	return LockJournalSequenceWith(directory, name, LockOps{})
}

func LockJournalSequenceWith(directory *os.File, name string, ops LockOps) (func(), error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(int(directory.Fd()), name, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("open journal sequence lock: %w", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("journal sequence lock is not one regular file")
	}
	chmod := ops.Chmod
	if chmod == nil {
		chmod = unix.Fchmod
	}
	if err := chmod(fd, 0o600); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	flock := ops.Flock
	if flock == nil {
		flock = unix.Flock
	}
	if err := flock(fd, unix.LOCK_EX); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return func() {
		_ = flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
	}, nil
}

func ParsePromptHeader(content []byte) (PromptHeader, error) {
	text := string(content)
	if !strings.HasPrefix(text, "---\n") {
		return PromptHeader{}, fmt.Errorf("missing YAML frontmatter")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return PromptHeader{}, fmt.Errorf("unterminated YAML frontmatter")
	}
	var header PromptHeader
	if err := yaml.Unmarshal([]byte(text[4:4+end+1]), &header); err != nil {
		return PromptHeader{}, fmt.Errorf("parse frontmatter: %w", err)
	}
	switch header.Source {
	case PromptSourceHarness, PromptSourceAgent, PromptSourceHuman:
	default:
		return PromptHeader{}, fmt.Errorf("invalid prompt source %q", header.Source)
	}
	return header, nil
}

func PromptSlug(explicit string, body []byte) string {
	candidate := explicit
	if candidate == "" {
		line := strings.TrimSpace(string(body))
		if index := strings.IndexAny(line, "\r\n"); index >= 0 {
			line = line[:index]
		}
		candidate = line
	}
	var builder strings.Builder
	previousDash := false
	for _, r := range strings.ToLower(candidate) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			previousDash = false
		default:
			if !previousDash && builder.Len() > 0 {
				builder.WriteByte('-')
				previousDash = true
			}
		}
		if builder.Len() >= 40 {
			break
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if slug == "" {
		return "prompt"
	}
	return slug
}
