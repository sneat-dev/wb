package worktreeclaims

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sneat-dev/wb/internal/worktreelayout"
)

// PromptSnapshot is private local input. It is never part of a public projection.
type PromptSnapshot struct {
	Contents []byte
	Digest   string
}
type Options struct {
	EffortID, RunID, Initiator, AgentID, AgentRuntime, Model, CLI, Provider string
	TaskSummary, WBSessionID, OriginalPrompt                                string
	RequireOriginalPrompt                                                   bool
	AcquiredVia                                                             string
	Snapshot                                                                PromptSnapshot
}
type PromptMetadata struct {
	Version         int       `json:"version"`
	SHA256          string    `json:"sha256"`
	SourceReference string    `json:"source_reference"`
	CapturedAt      time.Time `json:"captured_at"`
}
type ExecutionIdentity struct{ Model, CLI, Provider string }
type OptionsPorts struct {
	Root             func(string) (string, error)
	OpenRun          func(string, string, string, bool) (*os.File, string, error)
	ReadBytesAt      func(*os.File, string) ([]byte, error)
	ReadJSONAt       func(*os.File, string, any) error
	OpenPrivateChild func(*os.File, string, bool) (*os.File, error)
	ValidateIdentity func(ExecutionIdentity) error
	AbsPath          func(string) (string, error)
	OpenPrompt       func(string) (*os.File, error)
	StatPrompt       func(*os.File) (os.FileInfo, error)
	ReadPrompt       func(*os.File) ([]byte, error)
}

const OriginalPromptStdinMarker = "(stdin)"
const MaxTaskSummaryRunes = 240

var credentialAssignment = regexp.MustCompile(`(?i)(password|secret|token|api[_-]?key)\s*[:=]\s*\S+`)
var credentialTokenMarker = regexp.MustCompile(`(?i)(^|[^a-z0-9])(sk-[a-z0-9_-]{8,}|akia[a-z0-9]{12,}|aiza[a-z0-9_-]{12,}|sk_[a-z0-9_-]{16,}|rk_live_[a-z0-9_-]{16,}|gh[opusr]_[a-z0-9_-]{16,}|github_pat_[a-z0-9_-]{16,}|glpat-[a-z0-9_-]{16,}|xox[abpr]-[a-z0-9_-]{16,}|npm_[a-z0-9_-]{24,}|pypi-[a-z0-9_-]{16,})`)
var bearerCredential = regexp.MustCompile(`(?i)(^|[^a-z0-9])bearer\s+[a-z0-9._~+/=-]{16,}`)

func (p OptionsPorts) PrepareOptions(projectsRoot, task string, options Options) (Options, error) {
	now := time.Now().UTC()
	effort, run, err := p.NormalizeOptions(task, options, now)
	if err != nil {
		return Options{}, err
	}
	options.EffortID = effort
	options.RunID = run
	if err := p.SnapshotOriginalPrompt(&options); err != nil {
		return Options{}, err
	}
	home, err := p.Root(projectsRoot)
	if err != nil {
		return Options{}, err
	}
	if err := p.CorroborateExistingRunPrompt(home, effort, run, options); err != nil {
		return Options{}, err
	}
	return options, nil
}

func (p OptionsPorts) PreflightOptions(task string, options Options) error {
	effort, run, err := p.NormalizeOptions(task, options, time.Now().UTC())
	if err != nil {
		return err
	}
	options.EffortID, options.RunID = effort, run
	return p.SnapshotOriginalPrompt(&options)
}

func (p OptionsPorts) SnapshotOriginalPrompt(options *Options) error {
	if len(options.Snapshot.Contents) != 0 {
		digest := sha256.Sum256(options.Snapshot.Contents)
		if options.Snapshot.Digest != hex.EncodeToString(digest[:]) || strings.TrimSpace(options.OriginalPrompt) == "" {
			return fmt.Errorf("prepared original prompt snapshot is internally inconsistent")
		}
		return nil
	}
	prompt := strings.TrimSpace(options.OriginalPrompt)
	if prompt == "" {
		if options.RequireOriginalPrompt {
			return fmt.Errorf("--original-prompt-file is required so the private Work Log can retain the exact originating request")
		}
		return nil
	}
	absPath := p.AbsPath
	if absPath == nil {
		absPath = filepath.Abs
	}
	absolute, err := absPath(prompt)
	if err != nil {
		return fmt.Errorf("resolve original prompt %s before mutation: %w", prompt, err)
	}
	openPrompt := p.OpenPrompt
	if openPrompt == nil {
		openPrompt = os.Open
	}
	file, err := openPrompt(absolute)
	if err != nil {
		return fmt.Errorf("open original prompt %s before mutation: %w", prompt, err)
	}
	defer func() { _ = file.Close() }()
	statPrompt := p.StatPrompt
	if statPrompt == nil {
		statPrompt = func(file *os.File) (os.FileInfo, error) { return file.Stat() }
	}
	info, err := statPrompt(file)
	if err != nil {
		return fmt.Errorf("inspect original prompt %s: %w", prompt, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("original prompt %s must be a regular file", prompt)
	}
	readPrompt := p.ReadPrompt
	if readPrompt == nil {
		readPrompt = func(file *os.File) ([]byte, error) { return io.ReadAll(file) }
	}
	contents, err := readPrompt(file)
	if err != nil {
		return fmt.Errorf("read original prompt %s before mutation: %w", prompt, err)
	}
	if len(bytes.TrimSpace(contents)) == 0 {
		return fmt.Errorf("original prompt %s must not be empty", prompt)
	}
	digest := sha256.Sum256(contents)
	options.OriginalPrompt = absolute
	options.Snapshot.Contents = append([]byte(nil), contents...)
	options.Snapshot.Digest = hex.EncodeToString(digest[:])
	return nil
}

func (p OptionsPorts) CorroborateExistingRunPrompt(home, effort, run string, options Options) error {
	runDir, _, err := p.OpenRun(home, effort, run, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect existing work-log run before mutation: %w", err)
	}
	defer func() { _ = runDir.Close() }()
	archived, promptErr := p.ReadBytesAt(runDir, "original-prompt.txt")
	var metadata PromptMetadata
	metadataErr := p.ReadJSONAt(runDir, "original-prompt.json", &metadata)
	if promptErr == nil {
		if len(options.Snapshot.Contents) == 0 {
			return fmt.Errorf("work-log run %s/%s already has an original prompt; provide the same --original-prompt-file", effort, run)
		}
		if !bytes.Equal(archived, options.Snapshot.Contents) {
			return fmt.Errorf("work-log run %s/%s is already bound to different original prompt bytes", effort, run)
		}
		digest := sha256.Sum256(archived)
		want := hex.EncodeToString(digest[:])
		if metadataErr == nil && (metadata.Version != 1 || metadata.SHA256 != want) {
			return fmt.Errorf("work-log run %s/%s prompt metadata does not match its immutable archive", effort, run)
		}
		if metadataErr != nil && !errors.Is(metadataErr, os.ErrNotExist) {
			return fmt.Errorf("inspect existing prompt metadata: %w", metadataErr)
		}
		return nil
	}
	if !errors.Is(promptErr, os.ErrNotExist) {
		return fmt.Errorf("inspect existing original prompt: %w", promptErr)
	}
	if metadataErr == nil || !errors.Is(metadataErr, os.ErrNotExist) {
		if metadataErr != nil {
			return fmt.Errorf("inspect existing prompt metadata: %w", metadataErr)
		}
		return fmt.Errorf("work-log run %s/%s has prompt metadata without its immutable archive", effort, run)
	}
	// Once a run index or claim exists, an absent prompt is evidence from an
	// older/partial writer. Never guess that a newly supplied file was the
	// original request and silently rewrite history.
	if _, err := p.ReadBytesAt(runDir, "run.json"); err == nil {
		return fmt.Errorf("work-log run %s/%s already exists without an immutable original prompt", effort, run)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	claims, err := p.OpenPrivateChild(runDir, "claims", false)
	if err == nil {
		defer func() { _ = claims.Close() }()
		if names, readErr := claims.Readdirnames(1); readErr == nil && len(names) != 0 {
			return fmt.Errorf("work-log run %s/%s already has a claim without an immutable original prompt", effort, run)
		} else if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (p OptionsPorts) NormalizeOptions(task string, options Options, now time.Time) (effort, run string, err error) {
	if err := p.ValidateIdentity(ExecutionIdentity{Model: options.Model, CLI: options.CLI, Provider: options.Provider}); err != nil {
		return "", "", err
	}
	effort = strings.TrimSpace(options.EffortID)
	if effort == "" {
		effort = task
	}
	run = strings.TrimSpace(options.RunID)
	if run == "" {
		run = "wb-" + now.Format("20060102T150405.000000000Z")
	}
	if !worktreelayout.ValidSafeSegment(effort) {
		return "", "", fmt.Errorf("work-log effort id %q must be one safe path segment", effort)
	}
	if !worktreelayout.ValidSafeSegment(run) {
		return "", "", fmt.Errorf("work-log run id %q must be one safe path segment", run)
	}
	if _, err := NormalizeTaskSummary(options.TaskSummary); err != nil {
		return "", "", err
	}
	return effort, run, nil
}

func NormalizeTaskSummary(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.ContainsAny(value, "\r\n") || !utf8.ValidString(value) {
		return "", errors.New("--summary must be one printable line")
	}
	if utf8.RuneCountInString(value) > MaxTaskSummaryRunes {
		return "", fmt.Errorf("--summary must be at most %d characters", MaxTaskSummaryRunes)
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return "", errors.New("--summary must be one printable line")
		}
	}
	lower := strings.ToLower(value)
	if bearerCredential.MatchString(lower) || credentialAssignment.MatchString(lower) || ContainsCredentialMarker(lower) {
		return "", errors.New("--summary must not contain a credential")
	}
	return value, nil
}

func ContainsCredentialMarker(lower string) bool {
	return credentialTokenMarker.MatchString(lower)
}

func (options Options) WithOriginalPromptFromStdin(content []byte) (Options, error) {
	if len(bytes.TrimSpace(content)) == 0 {
		return Options{}, fmt.Errorf("--original-prompt-file - requires non-empty stdin so the private Work Log can retain the exact originating request")
	}
	digest := sha256.Sum256(content)
	options.OriginalPrompt = OriginalPromptStdinMarker
	options.Snapshot.Contents = append([]byte(nil), content...)
	options.Snapshot.Digest = hex.EncodeToString(digest[:])
	return options, nil
}
