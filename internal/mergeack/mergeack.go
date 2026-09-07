// Package mergeack owns the on-disk shape, identity hash, and full
// validation of a worktree-merge absorbed-conflict acknowledgement -- the
// sidecar `wb worktree merge acknowledge-absorbed-conflict` writes next to a
// worktree-merge receipt whose every receipted source worktree is gone, once
// each source's content is proved already reachable from the current remote
// target.
//
// It is a leaf package: it imports nothing from internal/orchestrate or
// internal/worktrees, so both can depend on it without cycling. Both callers
// used to carry their own copy of this logic -- internal/orchestrate wrote
// and richly validated it, internal/worktrees re-validated a much smaller
// subset when deciding whether to accept an acknowledgement as cleanup
// landing proof -- which let a hand-edited sidecar with fabricated or
// emptied proofs slip past the weaker copy. This package is now the single
// source of truth for both the format and the validation, so a real sidecar
// written by internal/orchestrate validates byte-identically here, and a
// tampered one is rejected the same way regardless of which caller reads it.
package mergeack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// SchemaVersion is the current on-disk schema for Acknowledgement. There is
// exactly one definition of it in the whole module -- here -- so it can never
// drift between callers.
const SchemaVersion = 1

// Status is the only status value a valid Acknowledgement carries.
const Status = "absorbed_conflict_acknowledged"

// FileSuffix names the sidecar file relative to its receipt's own path:
// `<receipt path><FileSuffix>`.
const FileSuffix = ".absorbed-conflict.ack.json"

// Source identifies one worktree-merge source or candidate by its immutable
// task, worktree, branch, and commit SHA. It mirrors the fields
// internal/orchestrate.WorktreeMergeSource and WorktreeMergeCandidate carry in
// common, so a slice of either converts to []Source with a plain type
// conversion.
type Source struct {
	Task     string `json:"task"`
	Worktree string `json:"worktree"`
	Branch   string `json:"branch"`
	SHA      string `json:"sha"`
}

// PathProof records how one path a source changed relative to its merge-base
// with the current target was proved absorbed: "blob_absorbed" (identical git
// blob on the target), "lines_absorbed" (every line the source added to a
// `*.jsonl` append-only ledger, relative to the merge-base, is present
// verbatim as a line in the target's copy -- AddedLines and MatchedLines
// record the counts), or "derived_excused" (an operator-audited
// `--derived-path` exclusion for a known generated-index shape).
type PathProof struct {
	Path         string `json:"path"`
	Method       string `json:"method"`
	AddedLines   int    `json:"added_lines,omitempty"`
	MatchedLines int    `json:"matched_lines,omitempty"`
}

// SourceProof records, for one receipted source, how its content was proved
// already reachable from the current remote target: either the receipted
// source SHA is a direct ancestor of the freshly fetched target head
// ("ancestor"), or every path it changed relative to its merge-base with the
// target is individually proved absorbed ("content_absorbed"). MergeBaseSHA
// and PathCount are populated only for the content_absorbed method;
// PathProofs details how each changed path was individually proved.
type SourceProof struct {
	Task         string      `json:"task"`
	Worktree     string      `json:"worktree"`
	Branch       string      `json:"branch"`
	SHA          string      `json:"sha"`
	Method       string      `json:"method"`
	MergeBaseSHA string      `json:"merge_base_sha,omitempty"`
	PathCount    int         `json:"path_count,omitempty"`
	PathProofs   []PathProof `json:"path_proofs,omitempty"`
}

// Acknowledgement is a separate, append-only acknowledgement for an
// unpublished prepare-phase conflict receipt whose every receipted source
// worktree is already gone from disk, yet whose exact receipted content is
// proved, source by source, to already be reachable from the freshly fetched
// current remote target either by graph ancestry or by identical-blob
// content absorption. ReceiptStatus is carried as a plain string so this leaf
// package never needs internal/orchestrate's WorktreeMergeStatus type;
// callers convert at the boundary.
type Acknowledgement struct {
	SchemaVersion       int           `json:"schema_version"`
	ID                  string        `json:"id"`
	Status              string        `json:"status"`
	ReceiptPath         string        `json:"receipt_path"`
	AcknowledgementPath string        `json:"acknowledgement_path"`
	ReceiptID           string        `json:"receipt_id"`
	ReceiptSHA256       string        `json:"receipt_sha256"`
	ReceiptStatus       string        `json:"receipt_status"`
	Lane                string        `json:"lane"`
	Repository          string        `json:"repository"`
	Target              string        `json:"target"`
	ReceiptTargetSHA    string        `json:"receipt_target_sha"`
	CurrentTargetSHA    string        `json:"current_target_sha"`
	CandidateTask       string        `json:"candidate_task"`
	CandidateWorktree   string        `json:"candidate_worktree"`
	CandidateBranch     string        `json:"candidate_branch"`
	CandidateSHA        string        `json:"candidate_sha,omitempty"`
	Sources             []Source      `json:"sources"`
	SourceProofs        []SourceProof `json:"source_proofs"`
	ExcusedDerivedPaths []string      `json:"excused_derived_paths,omitempty"`
	Actor               string        `json:"actor"`
	Reason              string        `json:"reason"`
	RecordedAt          time.Time     `json:"recorded_at"`
}

// ReceiptIdentity is the subset of an internal/orchestrate.WorktreeMergeReceipt
// that Load validates an Acknowledgement against. Path is the receipt's own
// file path, used both as the immutable ReceiptPath identity and to compute
// ReceiptSHA256 fresh from the receipt's current bytes.
type ReceiptIdentity struct {
	Path       string
	ID         string
	Status     string
	Lane       string
	Repository string
	Target     string
	TargetSHA  string
	Candidate  Source
	Sources    []Source
}

// ComputeID hashes every recorded field of ack (excluding RecordedAt, which
// carries no evidentiary weight) into its content-addressed ID, including
// Actor and Reason -- a tamper that only rewrites who acknowledged it, or
// why, must still invalidate the ID. Same, below, compares evidence without
// Actor/Reason/ID/RecordedAt instead, so a genuine retry that repeats the
// same receipt, target, and per-source proof is still recognized as the same
// acknowledgement even when the operator supplies a different actor or
// reason on the replay.
func ComputeID(ack Acknowledgement) string {
	hash := sha256.New()
	for _, value := range []string{
		ack.ReceiptID, ack.ReceiptPath, ack.ReceiptSHA256, ack.ReceiptStatus, ack.Lane, ack.Repository, ack.Target,
		ack.ReceiptTargetSHA, ack.CurrentTargetSHA, ack.CandidateTask, ack.CandidateWorktree, ack.CandidateBranch, ack.CandidateSHA,
		ack.Actor, ack.Reason,
	} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	for _, source := range ack.Sources {
		for _, value := range []string{source.Task, source.Worktree, source.Branch, source.SHA} {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
	}
	_, _ = hash.Write([]byte{0xfe})
	for _, proof := range ack.SourceProofs {
		for _, value := range []string{proof.Task, proof.Worktree, proof.Branch, proof.SHA, proof.Method, proof.MergeBaseSHA} {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
		_, _ = hash.Write([]byte{byte(proof.PathCount)})
		_, _ = hash.Write([]byte{0xfd})
		for _, pathProof := range proof.PathProofs {
			for _, value := range []string{pathProof.Path, pathProof.Method} {
				_, _ = hash.Write([]byte(value))
				_, _ = hash.Write([]byte{0})
			}
			_, _ = hash.Write([]byte{byte(pathProof.AddedLines), byte(pathProof.MatchedLines)})
		}
	}
	_, _ = hash.Write([]byte{0xfc})
	for _, path := range ack.ExcusedDerivedPaths {
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// SameSources reports whether left and right name the same sources, in the
// same order, by task/worktree/branch/SHA.
func SameSources(left, right []Source) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameSourceProof(left, right SourceProof) bool {
	if left.Task != right.Task || left.Worktree != right.Worktree || left.Branch != right.Branch || left.SHA != right.SHA ||
		left.Method != right.Method || left.MergeBaseSHA != right.MergeBaseSHA || left.PathCount != right.PathCount ||
		len(left.PathProofs) != len(right.PathProofs) {
		return false
	}
	for index := range left.PathProofs {
		if left.PathProofs[index] != right.PathProofs[index] {
			return false
		}
	}
	return true
}

// Same compares only the immutable proof evidence two acknowledgements carry,
// deliberately excluding Actor/Reason/ID/RecordedAt: a retry that repeats the
// same receipt, target, and per-source proof is idempotent even when the
// operator supplies a different actor or reason on the replay.
func Same(left, right Acknowledgement) bool {
	if left.ReceiptPath != right.ReceiptPath || left.AcknowledgementPath != right.AcknowledgementPath ||
		left.ReceiptID != right.ReceiptID || left.ReceiptSHA256 != right.ReceiptSHA256 || left.ReceiptStatus != right.ReceiptStatus ||
		left.Lane != right.Lane || left.Repository != right.Repository || left.Target != right.Target ||
		left.ReceiptTargetSHA != right.ReceiptTargetSHA || left.CurrentTargetSHA != right.CurrentTargetSHA ||
		left.CandidateTask != right.CandidateTask || left.CandidateWorktree != right.CandidateWorktree ||
		left.CandidateBranch != right.CandidateBranch || left.CandidateSHA != right.CandidateSHA ||
		!SameSources(left.Sources, right.Sources) || len(left.SourceProofs) != len(right.SourceProofs) ||
		!slices.Equal(left.ExcusedDerivedPaths, right.ExcusedDerivedPaths) {
		return false
	}
	for index := range left.SourceProofs {
		if !sameSourceProof(left.SourceProofs[index], right.SourceProofs[index]) {
			return false
		}
	}
	return true
}

// IsDerivedPathAllowed narrowly allows the one derived-index shape this
// recovery may excuse: a generated spec/**/README.md listing index, exactly
// "README.md" nested anywhere under a repo-root "spec/" directory. Chosen
// over "any README.md the receipt's sources changed" because that would let
// an operator excuse an unrelated hand-authored README.md merely for
// appearing in this receipt; this shape check binds to the generated-index
// location instead, regardless of receipt contents.
func IsDerivedPathAllowed(path string) bool {
	segments := strings.Split(path, "/")
	if len(segments) < 2 || segments[0] != "spec" {
		return false
	}
	return segments[len(segments)-1] == "README.md"
}

// FileSHA256 hashes path's current bytes -- the same plain SHA-256 an
// Acknowledgement's ReceiptSHA256 binds to its receipt's exact, unchanged
// bytes.
func FileSHA256(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:]), nil
}

// Path names the acknowledgement sidecar for a receipt at receiptPath.
func Path(receiptPath string) string {
	return receiptPath + FileSuffix
}

// Persist atomically writes ack to path (create-temp, fsync, rename), the
// same durability shape every other WB receipt/sidecar uses.
func Persist(path string, ack Acknowledgement) error {
	contents, err := json.MarshalIndent(ack, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".absorbed-conflict-ack-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

// Load reads and fully validates the acknowledgement sidecar at path against
// receipt: every immutable identity field, the SHA-256 binding to receipt's
// exact current bytes, every source proof's identity and method, every
// excused derived path's shape and use, and the recomputed content hash ID.
// A sidecar that fails any of these checks -- including one that is merely
// missing -- returns a non-nil error; the two are distinguished with
// os.IsNotExist, exactly as os.ReadFile itself would report.
func Load(path string, receipt ReceiptIdentity) (Acknowledgement, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Acknowledgement{}, err
	}
	var ack Acknowledgement
	if err := json.Unmarshal(contents, &ack); err != nil {
		return Acknowledgement{}, fmt.Errorf("decode absorbed-conflict acknowledgement %s: %w", path, err)
	}
	receiptHash, err := FileSHA256(receipt.Path)
	if err != nil {
		return Acknowledgement{}, err
	}
	if ack.SchemaVersion != SchemaVersion || ack.Status != Status ||
		ack.AcknowledgementPath != path || ack.ReceiptPath != receipt.Path || ack.ReceiptID != receipt.ID || ack.ReceiptSHA256 != receiptHash ||
		ack.ReceiptStatus != receipt.Status || ack.Lane != receipt.Lane || ack.Repository != receipt.Repository || ack.Target != receipt.Target ||
		ack.ReceiptTargetSHA != receipt.TargetSHA || ack.CandidateTask != receipt.Candidate.Task || ack.CandidateWorktree != receipt.Candidate.Worktree ||
		ack.CandidateBranch != receipt.Candidate.Branch || ack.CandidateSHA != receipt.Candidate.SHA || !SameSources(ack.Sources, receipt.Sources) ||
		len(ack.SourceProofs) != len(receipt.Sources) || ack.CurrentTargetSHA == "" ||
		ack.Actor == "" || ack.Reason == "" || ack.RecordedAt.IsZero() || ack.ID != ComputeID(ack) {
		return Acknowledgement{}, fmt.Errorf("absorbed-conflict acknowledgement %s has invalid immutable identity", path)
	}
	excusedDerivedPaths := make(map[string]bool, len(ack.ExcusedDerivedPaths))
	for _, derivedPath := range ack.ExcusedDerivedPaths {
		if !IsDerivedPathAllowed(derivedPath) {
			return Acknowledgement{}, fmt.Errorf("absorbed-conflict acknowledgement %s excuses a derived path %q outside the allowed shape", path, derivedPath)
		}
		excusedDerivedPaths[derivedPath] = true
	}
	for index, proof := range ack.SourceProofs {
		source := receipt.Sources[index]
		if proof.Task != source.Task || proof.Worktree != source.Worktree || proof.Branch != source.Branch || proof.SHA != source.SHA {
			return Acknowledgement{}, fmt.Errorf("absorbed-conflict acknowledgement %s source proof identity drift", path)
		}
		if proof.Method != "ancestor" && proof.Method != "content_absorbed" {
			return Acknowledgement{}, fmt.Errorf("absorbed-conflict acknowledgement %s has an unknown proof method %q", path, proof.Method)
		}
		if proof.Method == "content_absorbed" && (proof.MergeBaseSHA == "" || proof.PathCount == 0 || len(proof.PathProofs) != proof.PathCount) {
			return Acknowledgement{}, fmt.Errorf("absorbed-conflict acknowledgement %s content-absorbed proof lacks its merge base, path count, or path proofs", path)
		}
		for _, pathProof := range proof.PathProofs {
			switch pathProof.Method {
			case "blob_absorbed":
			case "lines_absorbed":
				if pathProof.AddedLines != pathProof.MatchedLines {
					return Acknowledgement{}, fmt.Errorf("absorbed-conflict acknowledgement %s has an unproved lines-absorbed path %q", path, pathProof.Path)
				}
			case "derived_excused":
				if !excusedDerivedPaths[pathProof.Path] {
					return Acknowledgement{}, fmt.Errorf("absorbed-conflict acknowledgement %s excuses path %q that is not in its recorded excused derived paths", path, pathProof.Path)
				}
			default:
				return Acknowledgement{}, fmt.Errorf("absorbed-conflict acknowledgement %s has an unknown path proof method %q", path, pathProof.Method)
			}
		}
	}
	return ack, nil
}
