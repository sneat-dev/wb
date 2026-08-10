package worktrees

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WorkLogOptions is deliberately transport-neutral. WB keeps the durable
// journal and outbox locally, so agents can recover and coordinate through
// Git when a Synchestra server is unavailable. A future transport consumes the
// outbox; creation must not depend on a server being reachable.
type WorkLogOptions struct {
	EffortID       string
	RunID          string
	Initiator      string
	AgentID        string
	AgentRuntime   string
	Model          string
	OriginalPrompt string // local file; contents never enter the projection/outbox
}

type workLogRecord struct {
	Version       int       `json:"version"`
	EffortID      string    `json:"effort_id"`
	RunID         string    `json:"run_id"`
	Task          string    `json:"task"`
	Repository    string    `json:"repository"`
	Worktree      string    `json:"worktree"`
	Branch        string    `json:"branch"`
	Base          string    `json:"base"`
	BaseSHA       string    `json:"base_sha"`
	Lifecycle     string    `json:"lifecycle"`
	RecordedAt    time.Time `json:"recorded_at"`
	Initiator     string    `json:"initiator,omitempty"`
	AgentID       string    `json:"agent_id,omitempty"`
	AgentRuntime  string    `json:"agent_runtime,omitempty"`
	Model         string    `json:"model,omitempty"`
	PromptArchive string    `json:"prompt_archive,omitempty"`
}

type workLogTerminalRecord struct {
	workLogRecord
	FinalCommit string    `json:"final_commit"`
	Disposition string    `json:"worktree_disposition"`
	SealedAt    time.Time `json:"sealed_at"`
}

// recordWorkLog writes three separate evidence channels:
//   - a private, durable archive under WB_HOME (including a supplied prompt),
//   - a small Git-excluded projection in the worktree for crash recovery, and
//   - an append-only transport-neutral outbox event for Synchestra/Git sync.
//
// Worktree projections intentionally contain no prompt or history. Recycling
// can replace one safely without exposing the prior effort to the next agent.
func recordWorkLog(home, task string, result CreateResult, options WorkLogOptions) (string, error) {
	now := time.Now().UTC()
	effort := strings.TrimSpace(options.EffortID)
	if effort == "" {
		effort = task
	}
	if !validSafeSegment(effort) {
		return "", fmt.Errorf("work-log effort id %q must be one safe path segment", effort)
	}
	run := strings.TrimSpace(options.RunID)
	if run == "" {
		run = "wb-" + now.Format("20060102T150405.000000000Z")
	}
	if !validSafeSegment(run) {
		return "", fmt.Errorf("work-log run id %q must be one safe path segment", run)
	}
	dir := filepath.Join(home, "worklogs", effort, "runs", run)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create private work-log directory: %w", err)
	}
	record := workLogRecord{Version: 1, EffortID: effort, RunID: run, Task: task,
		Repository: result.Repository, Worktree: result.WorktreeDir, Branch: result.Branch,
		Base: result.Base, BaseSHA: result.BaseSHA, Lifecycle: "active", RecordedAt: now,
		Initiator: strings.TrimSpace(options.Initiator), AgentID: strings.TrimSpace(options.AgentID),
		AgentRuntime: strings.TrimSpace(options.AgentRuntime), Model: strings.TrimSpace(options.Model)}
	if prompt := strings.TrimSpace(options.OriginalPrompt); prompt != "" {
		contents, err := os.ReadFile(prompt)
		if err != nil {
			return "", fmt.Errorf("read original prompt %s: %w", prompt, err)
		}
		promptPath := filepath.Join(dir, "original-prompt.txt")
		if err := os.WriteFile(promptPath, contents, 0o600); err != nil {
			return "", fmt.Errorf("archive original prompt: %w", err)
		}
		record.PromptArchive = promptPath
	}
	// One run can claim several repositories. A claim is therefore immutable
	// per repository rather than a mutable run-level singleton that would let
	// the last repository silently overwrite the earlier ones.
	claims := filepath.Join(dir, "claims")
	if err := os.MkdirAll(claims, 0o700); err != nil {
		return "", fmt.Errorf("create private work-log claims: %w", err)
	}
	path := filepath.Join(claims, strings.ReplaceAll(result.Repository, "/", "-")+".json")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("work-log claim already exists for %s in run %s", result.Repository, run)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect existing work-log claim: %w", err)
	}
	if err := writeJSONAtomic(path, record, 0o600); err != nil {
		return "", err
	}
	// The run index is a stable recovery pointer, not a mutable list of
	// claims. Listing claims is deterministic and has no append race.
	if err := writeJSONAtomic(filepath.Join(dir, "run.json"), struct {
		Version int       `json:"version"`
		Effort  string    `json:"effort_id"`
		Run     string    `json:"run_id"`
		Created time.Time `json:"created_at"`
	}{Version: 1, Effort: effort, Run: run, Created: now}, 0o600); err != nil {
		return "", err
	}
	if err := writeWorkLogProjection(result.WorktreeDir, record); err != nil {
		return "", err
	}
	outbox := filepath.Join(home, "worklogs", effort, "outbox")
	if err := os.MkdirAll(outbox, 0o700); err != nil {
		return "", fmt.Errorf("create work-log outbox: %w", err)
	}
	event := struct {
		Type string        `json:"type"`
		At   time.Time     `json:"at"`
		Data workLogRecord `json:"data"`
	}{Type: "worktree.claimed", At: now, Data: record}
	if err := writeJSONAtomic(filepath.Join(outbox, run+"-"+strings.ReplaceAll(result.Repository, "/", "-")+".json"), event, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func writeWorkLogProjection(worktree string, record workLogRecord) error {
	const projection = ".wb-worklog.json"
	gitPath, err := git(context.Background(), worktree, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("resolve per-worktree exclude: %w", err)
	}
	if !filepath.IsAbs(gitPath) {
		gitPath = filepath.Join(worktree, gitPath)
	}
	if err := os.MkdirAll(filepath.Dir(gitPath), 0o700); err != nil {
		return fmt.Errorf("create per-worktree git info: %w", err)
	}
	exclude, err := os.ReadFile(gitPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read per-worktree exclude: %w", err)
	}
	if !strings.Contains("\n"+string(exclude)+"\n", "\n"+projection+"\n") {
		if err := os.WriteFile(gitPath, append(exclude, []byte("\n"+projection+"\n")...), 0o600); err != nil {
			return fmt.Errorf("exclude work-log projection: %w", err)
		}
	}
	return writeJSONAtomic(filepath.Join(worktree, projection), record, 0o600)
}

// sealWorkLogForRecycle archives terminal evidence before the worktree path is
// moved or removed. Absence is tolerated for pre-work-log worktrees; once a
// projection exists, failure to seal is fatal so a recycle cannot erase the
// only recovery clue. Upload is intentionally an outbox event, never a
// prerequisite for local cleanup while Synchestra is unavailable.
func sealWorkLogForRecycle(home, worktree, finalCommit, disposition string) error {
	projectionPath := filepath.Join(worktree, ".wb-worklog.json")
	contents, err := os.ReadFile(projectionPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var record workLogRecord
	if err := json.Unmarshal(contents, &record); err != nil {
		return fmt.Errorf("decode work-log projection: %w", err)
	}
	if record.EffortID == "" || record.RunID == "" {
		return fmt.Errorf("work-log projection lacks effort_id or run_id")
	}
	terminal := workLogTerminalRecord{workLogRecord: record, FinalCommit: finalCommit, Disposition: disposition, SealedAt: time.Now().UTC()}
	dir := filepath.Join(home, "worklogs", record.EffortID, "runs", record.RunID, "terminals")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(dir, strings.ReplaceAll(record.Repository, "/", "-")+".json"), terminal, 0o600); err != nil {
		return err
	}
	outbox := filepath.Join(home, "worklogs", record.EffortID, "outbox")
	if err := os.MkdirAll(outbox, 0o700); err != nil {
		return err
	}
	event := struct {
		Type string                `json:"type"`
		At   time.Time             `json:"at"`
		Data workLogTerminalRecord `json:"data"`
	}{Type: "worktree.sealed", At: terminal.SealedAt, Data: terminal}
	return writeJSONAtomic(filepath.Join(outbox, record.RunID+"-sealed.json"), event, 0o600)
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	content = append(content, '\n')
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(temporary, mode); err != nil {
		return fmt.Errorf("protect %s: %w", temporary, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("activate %s: %w", path, err)
	}
	return nil
}
