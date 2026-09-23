package worktrees

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
)

// RetirementPayload is the private, claim-scoped input for a later archive
// publisher. The plaintext archive and metadata are deterministic; age seals
// deliberately vary per call. This package has no key discovery, remote target,
// publication, cleanup, or retention-enforcement dependency.
type RetirementPayload struct {
	Metadata RetirementPayloadMetadata
	Sealed   []byte
}

// RetirementPayloadMetadata is safe to retain beside a sealed payload. It never
// contains a worktree path, prompt body, or remote readiness assertion.
type RetirementPayloadMetadata struct {
	Version         int    `json:"version"`
	EffortID        string `json:"effort_id"`
	RunID           string `json:"run_id"`
	ClaimID         string `json:"claim_id"`
	Retention       string `json:"retention"`
	FileCount       int    `json:"file_count"`
	PlaintextSHA256 string `json:"plaintext_sha256"`
}

// PackRetirementPayload deterministically bundles the local journal and the
// matching private Work Log run, then encrypts the bundle for recipient.
func PackRetirementPayload(worktree, home string, recipient age.Recipient) (RetirementPayload, error) {
	if recipient == nil {
		return RetirementPayload{}, fmt.Errorf("retirement payload requires an age recipient")
	}
	projection, err := readWorkLogProjectionForClaim(home, worktree)
	if err != nil {
		return RetirementPayload{}, fmt.Errorf("read claim-scoped work-log projection: %w", err)
	}
	if !validSafeSegment(projection.EffortID) || !validSafeSegment(projection.RunID) || !validSafeSegment(projection.ClaimID) {
		return RetirementPayload{}, fmt.Errorf("invalid claim identity for retirement payload")
	}
	runRoot := filepath.Join(home, "worklogs", projection.EffortID, "runs", projection.RunID)
	journalRoot := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory)
	files, err := retirementPayloadFiles(journalRoot, runRoot, projection.ClaimID)
	if err != nil {
		return RetirementPayload{}, err
	}
	plain, err := writeRetirementTar(files)
	if err != nil {
		return RetirementPayload{}, err
	}
	sum := sha256.Sum256(plain)
	metadata := RetirementPayloadMetadata{
		Version: 1, EffortID: projection.EffortID, RunID: projection.RunID, ClaimID: projection.ClaimID,
		Retention: "retain-until-explicit-private-archive-policy-decision", FileCount: len(files), PlaintextSHA256: hex.EncodeToString(sum[:]),
	}
	var sealed bytes.Buffer
	writer, err := age.Encrypt(&sealed, recipient)
	if err != nil {
		return RetirementPayload{}, fmt.Errorf("encrypt retirement payload: %w", err)
	}
	if _, err := writer.Write(plain); err != nil {
		return RetirementPayload{}, fmt.Errorf("write encrypted retirement payload: %w", err)
	}
	if err := writer.Close(); err != nil {
		return RetirementPayload{}, fmt.Errorf("seal retirement payload: %w", err)
	}
	return RetirementPayload{Metadata: metadata, Sealed: sealed.Bytes()}, nil
}

// VerifyRetirementPayload decrypts and validates a local payload before any
// later archive stage may use it. It rejects traversal, links, duplicates and
// files outside the claim-scoped allowlist.
func VerifyRetirementPayload(payload RetirementPayload, identity age.Identity) error {
	if identity == nil || payload.Metadata.Version != 1 || payload.Metadata.Retention != "retain-until-explicit-private-archive-policy-decision" || payload.Metadata.FileCount < 1 ||
		!validSafeSegment(payload.Metadata.EffortID) || !validSafeSegment(payload.Metadata.RunID) || !validSafeSegment(payload.Metadata.ClaimID) {
		return fmt.Errorf("invalid retirement payload metadata")
	}
	reader, err := age.Decrypt(bytes.NewReader(payload.Sealed), identity)
	if err != nil {
		return fmt.Errorf("decrypt retirement payload: %w", err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("read retirement payload: %w", err)
	}
	sum := sha256.Sum256(plain)
	if hex.EncodeToString(sum[:]) != payload.Metadata.PlaintextSHA256 {
		return fmt.Errorf("retirement payload integrity digest mismatch")
	}
	tr := tar.NewReader(bytes.NewReader(plain))
	seen := map[string]struct{}{}
	count := 0
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read retirement payload archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || header.Linkname != "" || header.Size < 0 || !validRetirementPayloadName(header.Name, payload.Metadata.ClaimID) {
			return fmt.Errorf("unsafe retirement payload entry %q", header.Name)
		}
		if _, duplicate := seen[header.Name]; duplicate {
			return fmt.Errorf("duplicate retirement payload entry %q", header.Name)
		}
		seen[header.Name] = struct{}{}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return fmt.Errorf("read retirement payload entry %q: %w", header.Name, err)
		}
		count++
	}
	if count != payload.Metadata.FileCount {
		return fmt.Errorf("retirement payload file count mismatch")
	}
	return nil
}

type retirementPayloadFile struct {
	name    string
	content []byte
}

func retirementPayloadFiles(journalRoot, runRoot, claimID string) ([]retirementPayloadFile, error) {
	var files []retirementPayloadFile
	for _, source := range []struct{ root, prefix string }{{journalRoot, "journal"}, {runRoot, "run"}} {
		entries, err := collectRetirementPayloadFiles(source.root, source.prefix, claimID)
		if err != nil {
			return nil, err
		}
		files = append(files, entries...)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, nil
}

func collectRetirementPayloadFiles(root, prefix, claimID string) ([]retirementPayloadFile, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if rootInfo.Mode()&fs.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, fmt.Errorf("retirement payload refuses unsafe source root %s", root)
	}
	var files []retirementPayloadFile
	err = filepath.WalkDir(root, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, filename)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("retirement payload refuses symlink %s", filename)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("retirement payload refuses non-regular file %s", filename)
		}
		name := path.Join(prefix, filepath.ToSlash(rel))
		if !validRetirementPayloadName(name, claimID) {
			return fmt.Errorf("retirement payload refuses unallowlisted file %s", name)
		}
		content, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		files = append(files, retirementPayloadFile{name: name, content: content})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func validRetirementPayloadName(name, claimID string) bool {
	if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	if strings.HasPrefix(name, "journal/") {
		rel := strings.TrimPrefix(name, "journal/")
		return rel == manifestName || rel == heartbeatName || rel == "prompts/.prompts.lock" || rel == "worklog/.journal.lock" ||
			promptFileName.MatchString(path.Base(rel)) && path.Dir(rel) == promptsDirectory ||
			(path.Dir(rel) == worklogDirectory && (path.Base(rel) == localWorkLogEventsName || path.Base(rel) == localWorkLogProjectionName || path.Base(rel) == localWorkLogOutboxName))
	}
	if !strings.HasPrefix(name, "run/") {
		return false
	}
	rel := strings.TrimPrefix(name, "run/")
	return rel == "run.json" || rel == "original-prompt.json" || rel == "original-prompt.txt" ||
		rel == "claims/"+claimID+".json" || rel == "terminals/"+claimID+".json" || rel == "cleanups/"+claimID+".json" ||
		(strings.HasPrefix(rel, "corrections/"+claimID+"/") && strings.HasSuffix(rel, ".json") && path.Base(rel) != ".json")
}

func writeRetirementTar(files []retirementPayloadFile) ([]byte, error) {
	var output bytes.Buffer
	tw := tar.NewWriter(&output)
	for _, file := range files {
		header := &tar.Header{Name: file.name, Mode: 0o600, Size: int64(len(file.content)), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
		if err := tw.WriteHeader(header); err != nil {
			return nil, err
		}
		if _, err := tw.Write(file.content); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
