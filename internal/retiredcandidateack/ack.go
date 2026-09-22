// Package retiredcandidateack owns the candidate-only acknowledgement used to
// retire an unpublished integration candidate after its recorded target was
// deleted. It deliberately contains no source-absorption assertion.
package retiredcandidateack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	SchemaVersion = 1
	Status        = "retired_prepare_candidate_acknowledged"
	FileSuffix    = ".retired-prepare-candidate.ack.json"
)

type Source struct {
	Task     string `json:"task"`
	Worktree string `json:"worktree"`
	Branch   string `json:"branch"`
	SHA      string `json:"sha"`
}

type ReceiptIdentity struct {
	Path, ID, Phase, Status, Lane, Repository, Target, TargetSHA string
	Candidate                                                    Source
	Sources                                                      []Source
}

type Acknowledgement struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	Status        string    `json:"status"`
	ReceiptPath   string    `json:"receipt_path"`
	ReceiptSHA256 string    `json:"receipt_sha256"`
	ReceiptID     string    `json:"receipt_id"`
	ReceiptPhase  string    `json:"receipt_phase"`
	ReceiptStatus string    `json:"receipt_status"`
	Lane          string    `json:"lane"`
	Repository    string    `json:"repository"`
	Target        string    `json:"target"`
	TargetSHA     string    `json:"target_sha"`
	Candidate     Source    `json:"candidate"`
	Sources       []Source  `json:"sources"`
	DefaultBranch string    `json:"default_branch"`
	DefaultSHA    string    `json:"default_sha"`
	Actor         string    `json:"actor"`
	Reason        string    `json:"reason"`
	RecordedAt    time.Time `json:"recorded_at"`
}

func Path(receiptPath string) string { return receiptPath + FileSuffix }

func FileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}

func ComputeID(a Acknowledgement) string {
	h := sha256.New()
	for _, v := range []string{a.ReceiptPath, a.ReceiptSHA256, a.ReceiptID, a.ReceiptPhase, a.ReceiptStatus, a.Lane, a.Repository, a.Target, a.TargetSHA, a.Candidate.Task, a.Candidate.Worktree, a.Candidate.Branch, a.Candidate.SHA, a.DefaultBranch, a.DefaultSHA, a.Actor, a.Reason} {
		_, _ = h.Write([]byte(v))
		_, _ = h.Write([]byte{0})
	}
	for _, s := range a.Sources {
		for _, v := range []string{s.Task, s.Worktree, s.Branch, s.SHA} {
			_, _ = h.Write([]byte(v))
			_, _ = h.Write([]byte{0})
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func Load(path string, id ReceiptIdentity) (Acknowledgement, error) {
	var a Acknowledgement
	b, err := os.ReadFile(path)
	if err != nil {
		return a, err
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return a, err
	}
	hash, err := FileSHA256(id.Path)
	if err != nil {
		return a, err
	}
	if a.SchemaVersion != SchemaVersion || a.Status != Status || a.ReceiptPath != id.Path || a.ReceiptSHA256 != hash || a.ReceiptID != id.ID || a.ReceiptPhase != id.Phase || a.ReceiptStatus != id.Status || a.Lane != id.Lane || a.Repository != id.Repository || a.Target != id.Target || a.TargetSHA != id.TargetSHA || a.Candidate != id.Candidate || !sameSources(a.Sources, id.Sources) || a.Candidate.SHA != id.TargetSHA || a.DefaultBranch == "" || a.DefaultSHA == "" || strings.TrimSpace(a.Actor) == "" || strings.TrimSpace(a.Reason) == "" || a.RecordedAt.IsZero() || a.ID != ComputeID(a) {
		return a, fmt.Errorf("invalid retired prepare candidate acknowledgement")
	}
	return a, nil
}

func Persist(path string, a Acknowledgement) error {
	if a.ID == "" {
		a.ID = ComputeID(a)
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(b); err != nil {
		return err
	}
	return f.Sync()
}

func sameSources(a, b []Source) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func DistinctCandidate(candidate Source, sources []Source) bool {
	if candidate.Task == "" || candidate.Worktree == "" || candidate.Branch == "" || candidate.SHA == "" || len(sources) == 0 {
		return false
	}
	for _, s := range sources {
		if s.Task == "" || s.Worktree == "" || s.Branch == "" || s.SHA == "" || s.Task == candidate.Task || filepath.Clean(s.Worktree) == filepath.Clean(candidate.Worktree) || s.Branch == candidate.Branch || strings.TrimSpace(s.SHA) == candidate.SHA {
			return false
		}
	}
	return true
}
