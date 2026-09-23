package worktrees

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

const (
	retirementMaxFileBytes  int64 = 32 << 20
	retirementMaxTotalBytes int64 = 96 << 20
	retirementMaxFiles            = 4096
)

// RetirementPayloadExpectation must be persisted by the caller from a trusted
// Pack result or source receipt independently of the payload being verified.
// In particular, never copy PlaintextSHA256 from untrusted payload metadata:
// an age recipient is public and does not authenticate the archive producer.
type RetirementPayloadExpectation struct {
	EffortID, RunID, ClaimID string
	Worktree, Repository     string
	PlaintextSHA256          string
}

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
	localProjection, err := readRetirementLocalProjection(worktree)
	if err != nil {
		return RetirementPayload{}, fmt.Errorf("read claim-scoped work-log projection: %w", err)
	}
	projection := workLogProjection{EffortID: localProjection.EffortID, RunID: localProjection.RunID, ClaimID: localProjection.ClaimID}
	if !validSafeSegment(projection.EffortID) || !validSafeSegment(projection.RunID) || !validSafeSegment(projection.ClaimID) {
		return RetirementPayload{}, fmt.Errorf("invalid claim identity for retirement payload")
	}
	terminal, err := validateRetirementPayloadSource(worktree, home, projection)
	if err != nil {
		return RetirementPayload{}, err
	}
	files, closeSources, err := retirementPayloadFiles(worktree, home, projection, terminal)
	if err != nil {
		return RetirementPayload{}, err
	}
	defer closeSources()
	var sealed bytes.Buffer
	writer, err := age.Encrypt(&sealed, recipient)
	if err != nil {
		return RetirementPayload{}, fmt.Errorf("encrypt retirement payload: %w", err)
	}
	hasher := sha256.New()
	if err := writeRetirementTar(files, io.MultiWriter(writer, hasher)); err != nil {
		_ = writer.Close()
		return RetirementPayload{}, err
	}
	if err := writer.Close(); err != nil {
		return RetirementPayload{}, fmt.Errorf("seal retirement payload: %w", err)
	}
	metadata := RetirementPayloadMetadata{
		Version: 1, EffortID: projection.EffortID, RunID: projection.RunID, ClaimID: projection.ClaimID,
		Retention: "retain-until-explicit-private-archive-policy-decision", FileCount: len(files), PlaintextSHA256: hex.EncodeToString(hasher.Sum(nil)),
	}
	return RetirementPayload{Metadata: metadata, Sealed: sealed.Bytes()}, nil
}

// VerifyRetirementPayload decrypts and validates a local payload before any
// later archive stage may use it. It rejects traversal, links, duplicates and
// files outside the claim-scoped allowlist.
func VerifyRetirementPayload(payload RetirementPayload, identity age.Identity, expected RetirementPayloadExpectation) error {
	if identity == nil || payload.Metadata.Version != 1 || payload.Metadata.Retention != "retain-until-explicit-private-archive-policy-decision" || payload.Metadata.FileCount < 1 ||
		payload.Metadata.FileCount > retirementMaxFiles || !validSafeSegment(expected.EffortID) || !validSafeSegment(expected.RunID) || !validSafeSegment(expected.ClaimID) ||
		expected.Worktree == "" || expected.Repository == "" || len(expected.PlaintextSHA256) != sha256.Size*2 || payload.Metadata.PlaintextSHA256 != expected.PlaintextSHA256 ||
		payload.Metadata.EffortID != expected.EffortID || payload.Metadata.RunID != expected.RunID || payload.Metadata.ClaimID != expected.ClaimID {
		return fmt.Errorf("invalid retirement payload metadata")
	}
	reader, err := age.Decrypt(bytes.NewReader(payload.Sealed), identity)
	if err != nil {
		return fmt.Errorf("decrypt retirement payload: %w", err)
	}
	hasher := sha256.New()
	limited := &io.LimitedReader{R: reader, N: retirementMaxTotalBytes + int64(retirementMaxFiles)*4096 + 4096}
	tr := tar.NewReader(io.TeeReader(limited, hasher))
	seen := map[string]struct{}{}
	contents := map[string][]byte{}
	digests := map[string]string{}
	sizes := map[string]int64{}
	count := 0
	var total int64
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read retirement payload archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || header.Linkname != "" || header.Size < 0 || header.Size > retirementMaxFileBytes || total+header.Size > retirementMaxTotalBytes || !validRetirementPayloadName(header.Name, payload.Metadata.ClaimID) {
			return fmt.Errorf("unsafe retirement payload entry %q", header.Name)
		}
		if _, duplicate := seen[header.Name]; duplicate {
			return fmt.Errorf("duplicate retirement payload entry %q", header.Name)
		}
		seen[header.Name] = struct{}{}
		sizes[header.Name] = header.Size
		if retirementRecordForValidation(header.Name, expected.ClaimID) {
			if header.Size > 2<<20 {
				return fmt.Errorf("retirement payload record too large %q", header.Name)
			}
			content, err := io.ReadAll(tr)
			if err != nil {
				return fmt.Errorf("read retirement payload entry %q: %w", header.Name, err)
			}
			contents[header.Name] = content
		} else {
			fileHash := sha256.New()
			if _, err := io.Copy(fileHash, tr); err != nil {
				return fmt.Errorf("read retirement payload entry %q: %w", header.Name, err)
			}
			if header.Name == "run/original-prompt.txt" || strings.HasPrefix(header.Name, "run/dirty-discard/"+expected.ClaimID+"/") {
				digests[header.Name] = hex.EncodeToString(fileHash.Sum(nil))
			}
		}
		total += header.Size
		count++
		if count > retirementMaxFiles {
			return fmt.Errorf("retirement payload has too many files")
		}
	}
	// tar.Reader stops at the end marker. Drain it to hash and bound all padding
	// and trailing bytes as well, including age's final authenticated chunk.
	if _, err := io.Copy(io.Discard, io.TeeReader(limited, hasher)); err != nil {
		return fmt.Errorf("drain retirement payload: %w", err)
	}
	if limited.N == 0 {
		return fmt.Errorf("retirement payload exceeds aggregate limit")
	}
	if hex.EncodeToString(hasher.Sum(nil)) != payload.Metadata.PlaintextSHA256 {
		return fmt.Errorf("retirement payload integrity digest mismatch")
	}
	if count != payload.Metadata.FileCount {
		return fmt.Errorf("retirement payload file count mismatch")
	}
	return validateRetirementPayloadContents(seen, contents, digests, sizes, payload.Metadata, expected)
}

type retirementPayloadFile struct {
	name string
	rel  string
	root *os.File
	size int64
}

func retirementPayloadFiles(worktree, home string, projection workLogProjection, terminal workLogTerminalRecord) ([]retirementPayloadFile, func(), error) {
	journal, err := openJournalDirectory(worktree, false)
	if err != nil {
		return nil, nil, fmt.Errorf("open retirement journal: %w", err)
	}
	run, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		_ = journal.Close()
		return nil, nil, fmt.Errorf("open retirement run: %w", err)
	}
	closeSources := func() { _ = journal.Close(); _ = run.Close() }
	var files []retirementPayloadFile
	var total int64
	for _, source := range []struct {
		root   *os.File
		prefix string
	}{{journal, "journal"}, {run, "run"}} {
		entries, err := collectRetirementPayloadFiles(source.root, source.prefix, projection.ClaimID, terminal, &total)
		if err != nil {
			closeSources()
			return nil, nil, err
		}
		files = append(files, entries...)
	}
	if len(files) > retirementMaxFiles {
		closeSources()
		return nil, nil, fmt.Errorf("retirement payload has too many files")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		seen[file.name] = struct{}{}
	}
	for _, required := range []string{"journal/manifest.yaml", "journal/worklog/events.jsonl", "journal/worklog/projection.json", "run/run.json", "run/original-prompt.json", "run/original-prompt.txt", "run/claims/" + projection.ClaimID + ".json", "run/terminals/" + projection.ClaimID + ".json", "run/locks/" + projection.ClaimID + ".lock"} {
		if _, ok := seen[required]; !ok {
			closeSources()
			return nil, nil, fmt.Errorf("retirement payload missing required source %q", required)
		}
	}
	hasPrompt := false
	for name := range seen {
		if strings.HasPrefix(name, "journal/prompts/") && promptFileName.MatchString(path.Base(name)) {
			hasPrompt = true
			break
		}
	}
	if !hasPrompt {
		closeSources()
		return nil, nil, fmt.Errorf("retirement payload missing local prompt")
	}
	if terminal.FinalizeReport != nil {
		if _, ok := seen["run/reports/"+filepath.Base(terminal.FinalizeReport.ReportPath)]; !ok {
			closeSources()
			return nil, nil, fmt.Errorf("retirement payload missing finalized report")
		}
	}
	if terminal.DirtyCapture != nil {
		if _, ok := seen["run/dirty-discard/"+projection.ClaimID+"/manifest.json"]; !ok {
			closeSources()
			return nil, nil, fmt.Errorf("retirement payload missing dirty capture")
		}
	}
	return files, closeSources, nil
}

func collectRetirementPayloadFiles(root *os.File, prefix, claimID string, terminal workLogTerminalRecord, total *int64) ([]retirementPayloadFile, error) {
	var files []retirementPayloadFile
	var walk func(*os.File, string) error
	walk = func(directory *os.File, relative string) error {
		names, err := directory.Readdirnames(retirementMaxFiles + 1)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(names) > retirementMaxFiles {
			return fmt.Errorf("retirement payload has too many directory entries")
		}
		sort.Strings(names)
		for _, name := range names {
			if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
				return fmt.Errorf("retirement payload refuses unsafe entry %q", name)
			}
			rel := path.Join(relative, name)
			if prefix == "run" && skipSiblingRetirementEntry(rel, claimID, terminal) {
				continue
			}
			var stat unix.Stat_t
			if err := unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return err
			}
			switch stat.Mode & unix.S_IFMT {
			case unix.S_IFDIR:
				child, err := openPrivateChild(directory, name, false)
				if err != nil {
					return fmt.Errorf("retirement payload refuses unsafe directory %s: %w", rel, err)
				}
				err = walk(child, rel)
				_ = child.Close()
				if err != nil {
					return err
				}
			case unix.S_IFREG:
				archiveName := path.Join(prefix, rel)
				if !validRetirementPayloadName(archiveName, claimID) || !validRetirementTerminalPath(archiveName, terminal) {
					return fmt.Errorf("retirement payload refuses unallowlisted file %s", archiveName)
				}
				file, err := openRetirementFile(directory, name)
				if err != nil {
					return err
				}
				info, err := file.Stat()
				_ = file.Close()
				if err != nil {
					return err
				}
				if !info.Mode().IsRegular() || info.Size() > retirementMaxFileBytes || *total+info.Size() > retirementMaxTotalBytes {
					return fmt.Errorf("retirement payload source exceeds file or aggregate limit: %s", archiveName)
				}
				*total += info.Size()
				files = append(files, retirementPayloadFile{name: archiveName, rel: rel, root: root, size: info.Size()})
				if len(files) > retirementMaxFiles {
					return fmt.Errorf("retirement payload has too many files")
				}
			default:
				return fmt.Errorf("retirement payload refuses symlink or non-regular file %s", rel)
			}
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return nil, err
	}
	return files, nil
}

func skipSiblingRetirementEntry(rel, claimID string, terminal workLogTerminalRecord) bool {
	parts := strings.Split(rel, "/")
	if len(parts) < 2 {
		return false
	}
	switch parts[0] {
	case "claims", "terminals", "cleanups", "locks":
		if len(parts) != 2 {
			return false
		}
		wanted := claimID + ".json"
		if parts[0] == "locks" {
			wanted = claimID + ".lock"
		}
		return parts[1] != wanted && validSafeSegment(strings.TrimSuffix(strings.TrimSuffix(parts[1], ".json"), ".lock"))
	case "corrections", "dirty-discard":
		return parts[1] != claimID && validSafeSegment(parts[1])
	case "reports":
		if len(parts) != 2 || !strings.HasSuffix(parts[1], ".md") {
			return false
		}
		return terminal.FinalizeReport == nil || parts[1] != filepath.Base(terminal.FinalizeReport.ReportPath)
	case "relocations":
		return len(parts) == 2 && !strings.HasPrefix(parts[1], claimID+"-") && validSafeSegment(parts[1])
	}
	return false
}

func openRetirementFile(directory *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("retirement payload refuses unsafe file %s: %w", name, err)
	}
	file := os.NewFile(uintptr(fd), name)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("retirement payload refuses non-regular file %s", name)
	}
	return file, nil
}

func openRetirementRelative(root *os.File, rel string) (*os.File, error) {
	parts := strings.Split(rel, "/")
	current := root
	for _, segment := range parts[:len(parts)-1] {
		child, err := openPrivateChild(current, segment, false)
		if current != root {
			_ = current.Close()
		}
		if err != nil {
			return nil, err
		}
		current = child
	}
	file, err := openRetirementFile(current, parts[len(parts)-1])
	if current != root {
		_ = current.Close()
	}
	return file, err
}

func validateRetirementPayloadSource(worktree, home string, projection workLogProjection) (workLogTerminalRecord, error) {
	manifest, err := readRetirementManifest(worktree)
	if err != nil || manifest.EffortID != projection.EffortID || manifest.RunID != projection.RunID || manifest.ClaimID != projection.ClaimID {
		return workLogTerminalRecord{}, fmt.Errorf("retirement payload requires matching local manifest")
	}
	run, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return workLogTerminalRecord{}, err
	}
	defer func() { _ = run.Close() }()
	claims, err := openPrivateChild(run, "claims", false)
	if err != nil {
		return workLogTerminalRecord{}, err
	}
	var claim workLogClaim
	err = readRetirementJSONAt(claims, projection.ClaimID+".json", &claim)
	_ = claims.Close()
	if err != nil || claim.EffortID != projection.EffortID || claim.RunID != projection.RunID || claim.ClaimID != projection.ClaimID {
		return workLogTerminalRecord{}, fmt.Errorf("retirement payload requires matching private claim")
	}
	if manifest.Worktree != claim.Worktree || manifest.Repository != claim.Repository {
		return workLogTerminalRecord{}, fmt.Errorf("retirement payload source identity mismatch")
	}
	if err := validateStaticWorkLogClaim(claim, projection.EffortID, projection.RunID); err != nil {
		return workLogTerminalRecord{}, fmt.Errorf("retirement payload private claim is invalid: %w", err)
	}
	journal, err := readRetirementRelocationJournal(run, claim)
	if err != nil {
		return workLogTerminalRecord{}, fmt.Errorf("retirement payload relocation evidence: %w", err)
	}
	currentPath, _, err := resolveRetirementLocation(claim, journal)
	if err != nil || currentPath != filepath.Clean(worktree) {
		return workLogTerminalRecord{}, fmt.Errorf("retirement payload current worktree lacks completed relocation binding: %v", err)
	}
	terminals, err := openPrivateChild(run, "terminals", false)
	if err != nil {
		return workLogTerminalRecord{}, fmt.Errorf("retirement payload requires terminal claim: %w", err)
	}
	var terminal workLogTerminalRecord
	err = readRetirementJSONAt(terminals, projection.ClaimID+".json", &terminal)
	_ = terminals.Close()
	if err != nil || terminal.EffortID != claim.EffortID || terminal.RunID != claim.RunID || terminal.ClaimID != claim.ClaimID {
		return workLogTerminalRecord{}, fmt.Errorf("retirement payload requires matching terminal claim")
	}
	if terminal.Worktree != claim.Worktree || terminal.Repository != claim.Repository || terminal.SealedAt.IsZero() {
		return workLogTerminalRecord{}, fmt.Errorf("retirement payload terminal identity mismatch")
	}
	expectedTerminalClaim := claim
	expectedTerminalClaim.Lifecycle = "terminal"
	if !reflect.DeepEqual(terminal.workLogClaim, expectedTerminalClaim) {
		return workLogTerminalRecord{}, fmt.Errorf("retirement payload terminal does not match private claim")
	}
	if terminal.FinalizeReport != nil && !validRetirementReportPath(terminal.FinalizeReport.ReportPath, claim) {
		return workLogTerminalRecord{}, fmt.Errorf("retirement payload terminal report path mismatch")
	}
	return terminal, nil
}

// Retirement preflight reads only small identity records. The later inventory
// enforces the payload-wide size limits before any prompt or report is copied.
func readRetirementBytesAt(directory *os.File, name string) ([]byte, error) {
	file, err := openRetirementFile(directory, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	const maxIdentityRecord = 2 << 20
	if info.Size() > maxIdentityRecord {
		return nil, fmt.Errorf("retirement payload identity record exceeds %d-byte limit: %s", maxIdentityRecord, name)
	}
	content, err := io.ReadAll(io.LimitReader(file, maxIdentityRecord+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxIdentityRecord {
		return nil, fmt.Errorf("retirement payload identity record exceeds %d-byte limit: %s", maxIdentityRecord, name)
	}
	return content, nil
}

func readRetirementJSONAt(directory *os.File, name string, target any) error {
	content, err := readRetirementBytesAt(directory, name)
	if err != nil {
		return err
	}
	return json.Unmarshal(content, target)
}

func readRetirementLocalProjection(worktree string) (LocalWorkLogProjection, error) {
	journal, err := openJournalDirectory(worktree, false)
	if err != nil {
		return LocalWorkLogProjection{}, err
	}
	defer func() { _ = journal.Close() }()
	worklog, err := openPrivateChild(journal, worklogDirectory, false)
	if err != nil {
		return LocalWorkLogProjection{}, err
	}
	defer func() { _ = worklog.Close() }()
	var projection LocalWorkLogProjection
	if err := readRetirementJSONAt(worklog, localWorkLogProjectionName, &projection); err != nil {
		return LocalWorkLogProjection{}, err
	}
	return projection, nil
}

func readRetirementManifest(worktree string) (Manifest, error) {
	journal, err := openJournalDirectory(worktree, false)
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = journal.Close() }()
	content, err := readRetirementBytesAt(journal, manifestName)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return Manifest{}, err
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func newRetirementRelocationJournal() relocationJournal {
	return relocationJournal{intents: map[string]workLogRelocationIntent{}, receipts: map[string]workLogRelocationReceipt{}}
}

func addRetirementRelocationRecord(journal *relocationJournal, claim workLogClaim, name string, content []byte) error {
	if !validRetirementRelocationName(name, claim.ClaimID) {
		return fmt.Errorf("unsafe relocation record %q", name)
	}
	var record workLogRelocationIntent
	if err := json.Unmarshal(content, &record); err != nil {
		return fmt.Errorf("decode relocation record %s: %w", name, err)
	}
	intent := strings.HasSuffix(name, ".intent.json")
	if err := validateRelocationRecord(record, claim, intent); err != nil {
		return fmt.Errorf("validate relocation record %s: %w", name, err)
	}
	want := relocationReceiptName(claim.ClaimID, record.OperationID)
	if intent {
		want = relocationIntentName(claim.ClaimID, record.OperationID)
	}
	if name != want {
		return fmt.Errorf("relocation filename does not bind operation %s", record.OperationID)
	}
	if intent {
		if _, duplicate := journal.intents[record.OperationID]; duplicate {
			return fmt.Errorf("duplicate relocation intent %s", record.OperationID)
		}
		journal.intents[record.OperationID] = record
	} else {
		if _, duplicate := journal.receipts[record.OperationID]; duplicate {
			return fmt.Errorf("duplicate relocation receipt %s", record.OperationID)
		}
		journal.receipts[record.OperationID] = record
	}
	return nil
}

func resolveRetirementLocation(claim workLogClaim, journal relocationJournal) (string, string, error) {
	current, repository := filepath.Clean(claim.Worktree), claim.Repository
	receipts := make([]workLogRelocationReceipt, 0, len(journal.receipts))
	for operationID, receipt := range journal.receipts {
		intent, ok := journal.intents[operationID]
		if !ok || !sameRelocationBinding(intent, receipt) {
			return "", "", fmt.Errorf("relocation receipt %s lacks its binding intent", operationID)
		}
		receipts = append(receipts, receipt)
	}
	sort.Slice(receipts, func(i, j int) bool {
		if receipts[i].At.Equal(receipts[j].At) {
			return receipts[i].OperationID < receipts[j].OperationID
		}
		return receipts[i].At.Before(receipts[j].At)
	})
	for _, receipt := range receipts {
		if filepath.Clean(receipt.Source) != current {
			return "", "", fmt.Errorf("relocation receipt %s breaks claim path chain", receipt.OperationID)
		}
		if receipt.To == "repository" || receipt.To == workLogRelocationLegacyCheckout {
			if receipt.SourceRepository != repository {
				return "", "", fmt.Errorf("relocation receipt %s breaks repository chain", receipt.OperationID)
			}
			repository = receipt.DestinationRepository
		}
		current = filepath.Clean(receipt.Destination)
	}
	return current, repository, nil
}

func readRetirementRelocationJournal(run *os.File, claim workLogClaim) (relocationJournal, error) {
	journal := newRetirementRelocationJournal()
	directory, err := openPrivateChild(run, "relocations", false)
	if errors.Is(err, os.ErrNotExist) {
		return journal, nil
	}
	if err != nil {
		return journal, err
	}
	defer func() { _ = directory.Close() }()
	names, err := directory.Readdirnames(retirementMaxFiles + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return journal, err
	}
	if len(names) > retirementMaxFiles {
		return journal, fmt.Errorf("too many relocation records")
	}
	var total int64
	for _, name := range names {
		if !strings.HasPrefix(name, claim.ClaimID+"-") {
			continue
		}
		content, err := readRetirementBytesAt(directory, name)
		if err != nil {
			return journal, err
		}
		total += int64(len(content))
		if total > retirementMaxTotalBytes {
			return journal, fmt.Errorf("relocation records exceed aggregate limit")
		}
		if err := addRetirementRelocationRecord(&journal, claim, name, content); err != nil {
			return journal, err
		}
	}
	_, _, err = resolveRetirementLocation(claim, journal)
	return journal, err
}

func archivedRetirementRelocationJournal(files map[string][]byte, claim workLogClaim) (relocationJournal, error) {
	journal := newRetirementRelocationJournal()
	for name, content := range files {
		if !strings.HasPrefix(name, "run/relocations/") {
			continue
		}
		if err := addRetirementRelocationRecord(&journal, claim, path.Base(name), content); err != nil {
			return journal, err
		}
	}
	_, _, err := resolveRetirementLocation(claim, journal)
	return journal, err
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
		rel == "claims/"+claimID+".json" || rel == "terminals/"+claimID+".json" || rel == "cleanups/"+claimID+".json" || rel == "locks/"+claimID+".lock" ||
		(path.Dir(rel) == "corrections/"+claimID && strings.HasSuffix(rel, ".json") && validSafeSegment(strings.TrimSuffix(path.Base(rel), ".json"))) ||
		(path.Dir(rel) == "reports" && strings.HasSuffix(rel, ".md") && validSafeSegment(strings.TrimSuffix(path.Base(rel), ".md"))) ||
		(path.Dir(rel) == "relocations" && validRetirementRelocationName(path.Base(rel), claimID)) ||
		(path.Dir(rel) == "dirty-discard/"+claimID && validSafeSegment(path.Base(rel)))
}

func validRetirementRelocationName(name, claimID string) bool {
	if !strings.HasPrefix(name, claimID+"-") {
		return false
	}
	rel := strings.TrimPrefix(name, claimID+"-")
	for _, suffix := range []string{".intent.json", ".completed.json"} {
		if strings.HasSuffix(rel, suffix) {
			return validSafeSegment(strings.TrimSuffix(rel, suffix))
		}
	}
	return false
}

func validRetirementTerminalPath(name string, terminal workLogTerminalRecord) bool {
	if !strings.HasPrefix(name, "run/") {
		return true
	}
	rel := strings.TrimPrefix(name, "run/")
	if strings.HasPrefix(rel, "reports/") {
		return terminal.FinalizeReport != nil && filepath.Base(terminal.FinalizeReport.ReportPath) == path.Base(rel)
	}
	if strings.HasPrefix(rel, "dirty-discard/") {
		return terminal.DirtyCapture != nil
	}
	return true
}

func retirementRecordForValidation(name, claimID string) bool {
	switch name {
	case "journal/manifest.yaml", "journal/worklog/projection.json", "run/run.json", "run/original-prompt.json", "run/claims/" + claimID + ".json", "run/terminals/" + claimID + ".json", "run/dirty-discard/" + claimID + "/manifest.json":
		return true
	}
	return strings.HasPrefix(name, "run/relocations/") && validRetirementRelocationName(path.Base(name), claimID)
}

func validateRetirementPayloadContents(seen map[string]struct{}, files map[string][]byte, digests map[string]string, sizes map[string]int64, metadata RetirementPayloadMetadata, expected RetirementPayloadExpectation) error {
	required := []string{"journal/manifest.yaml", "journal/worklog/events.jsonl", "journal/worklog/projection.json", "run/run.json", "run/original-prompt.json", "run/original-prompt.txt", "run/claims/" + metadata.ClaimID + ".json", "run/terminals/" + metadata.ClaimID + ".json", "run/locks/" + metadata.ClaimID + ".lock"}
	for _, name := range required {
		if _, ok := seen[name]; !ok {
			return fmt.Errorf("retirement payload missing required entry %q", name)
		}
	}
	hasPrompt := false
	for name := range seen {
		if strings.HasPrefix(name, "journal/prompts/") && promptFileName.MatchString(path.Base(name)) {
			hasPrompt = true
		}
	}
	if !hasPrompt {
		return fmt.Errorf("retirement payload missing local prompt")
	}
	var manifest Manifest
	var projection LocalWorkLogProjection
	var claim workLogClaim
	var terminal workLogTerminalRecord
	var prompt workLogPromptMetadata
	var run struct {
		Version  int    `json:"version"`
		EffortID string `json:"effort_id"`
		RunID    string `json:"run_id"`
	}
	if yaml.Unmarshal(files["journal/manifest.yaml"], &manifest) != nil || json.Unmarshal(files["journal/worklog/projection.json"], &projection) != nil || json.Unmarshal(files["run/claims/"+metadata.ClaimID+".json"], &claim) != nil || json.Unmarshal(files["run/terminals/"+metadata.ClaimID+".json"], &terminal) != nil {
		return fmt.Errorf("retirement payload contains invalid required claim records")
	}
	if json.Unmarshal(files["run/run.json"], &run) != nil {
		return fmt.Errorf("retirement payload contains invalid run record")
	}
	if json.Unmarshal(files["run/original-prompt.json"], &prompt) != nil || prompt.Version != 1 || prompt.SHA256 == "" || prompt.SHA256 != digests["run/original-prompt.txt"] {
		return fmt.Errorf("retirement payload original prompt digest mismatch")
	}
	if manifest.EffortID != expected.EffortID || manifest.RunID != expected.RunID || manifest.ClaimID != expected.ClaimID || manifest.Worktree != claim.Worktree || manifest.Repository != claim.Repository || projection.EffortID != expected.EffortID || projection.RunID != expected.RunID || projection.ClaimID != expected.ClaimID || claim.EffortID != expected.EffortID || claim.RunID != expected.RunID || claim.ClaimID != expected.ClaimID || terminal.EffortID != claim.EffortID || terminal.RunID != claim.RunID || terminal.ClaimID != claim.ClaimID || terminal.Worktree != claim.Worktree || terminal.Repository != claim.Repository || terminal.SealedAt.IsZero() || run.Version != 1 || run.EffortID != expected.EffortID || run.RunID != expected.RunID {
		return fmt.Errorf("retirement payload claim records do not bind metadata")
	}
	if claim.PromptDigest != "" && claim.PromptDigest != prompt.SHA256 {
		return fmt.Errorf("retirement payload claim prompt digest mismatch")
	}
	if err := validateStaticWorkLogClaim(claim, expected.EffortID, expected.RunID); err != nil {
		return fmt.Errorf("retirement payload private claim is invalid: %w", err)
	}
	journal, err := archivedRetirementRelocationJournal(files, claim)
	if err != nil {
		return fmt.Errorf("retirement payload relocation evidence: %w", err)
	}
	currentPath, currentRepository, err := resolveRetirementLocation(claim, journal)
	if err != nil || currentPath != filepath.Clean(expected.Worktree) || currentRepository != expected.Repository {
		return fmt.Errorf("retirement payload current source does not match trusted expectation: %v", err)
	}
	expectedTerminalClaim := claim
	expectedTerminalClaim.Lifecycle = "terminal"
	if !reflect.DeepEqual(terminal.workLogClaim, expectedTerminalClaim) {
		return fmt.Errorf("retirement payload terminal does not match private claim")
	}
	if terminal.FinalizeReport != nil {
		if !validRetirementReportPath(terminal.FinalizeReport.ReportPath, claim) {
			return fmt.Errorf("retirement payload report path does not bind claim")
		}
		if _, ok := seen["run/reports/"+filepath.Base(terminal.FinalizeReport.ReportPath)]; !ok {
			return fmt.Errorf("retirement payload missing finalized report")
		}
	}
	for name := range seen {
		if strings.HasPrefix(name, "run/reports/") && (terminal.FinalizeReport == nil || name != "run/reports/"+filepath.Base(terminal.FinalizeReport.ReportPath)) {
			return fmt.Errorf("retirement payload has unreferenced report")
		}
		if strings.HasPrefix(name, "run/dirty-discard/") && terminal.DirtyCapture == nil {
			return fmt.Errorf("retirement payload has unreferenced dirty capture")
		}
	}
	if terminal.DirtyCapture != nil {
		if _, ok := seen["run/dirty-discard/"+metadata.ClaimID+"/manifest.json"]; !ok {
			return fmt.Errorf("retirement payload missing dirty capture")
		}
		var dirty dirtyCaptureManifest
		if json.Unmarshal(files["run/dirty-discard/"+metadata.ClaimID+"/manifest.json"], &dirty) != nil || dirty.Receipt != *terminal.DirtyCapture || dirty.Version != 1 || dirty.Receipt.SHA256 != dirtyCaptureDigest(dirty.Entries) || dirty.Receipt.Files != len(dirty.Entries) {
			return fmt.Errorf("retirement payload dirty receipt does not bind terminal")
		}
		var dirtyBytes int64
		allowedDirty := map[string]bool{"run/dirty-discard/" + metadata.ClaimID + "/manifest.json": true}
		for _, entry := range dirty.Entries {
			dirtyBytes += entry.Bytes
			if entry.Blob != "" {
				if entry.Bytes < 0 || entry.Bytes > maxDirtyCaptureFileBytes || entry.Blob != dirtyCaptureBlobName(int(entry.Bytes), entry.SHA256) {
					return fmt.Errorf("retirement payload invalid dirty blob reference")
				}
				blobName := "run/dirty-discard/" + metadata.ClaimID + "/" + entry.Blob
				allowedDirty[blobName] = true
				if _, ok := seen[blobName]; !ok {
					return fmt.Errorf("retirement payload missing dirty blob")
				}
				if sizes[blobName] != entry.Bytes || digests[blobName] != entry.SHA256 {
					return fmt.Errorf("retirement payload dirty blob digest mismatch")
				}
			}
		}
		if dirtyBytes != dirty.Receipt.Bytes || dirtyBytes > maxDirtyCaptureTotalBytes {
			return fmt.Errorf("retirement payload dirty byte count mismatch")
		}
		for name := range seen {
			if strings.HasPrefix(name, "run/dirty-discard/"+metadata.ClaimID+"/") && !allowedDirty[name] {
				return fmt.Errorf("retirement payload has unreferenced dirty file")
			}
		}
	}
	return nil
}

func validRetirementReportPath(reportPath string, claim workLogClaim) bool {
	if reportPath == "" || filepath.Clean(reportPath) != reportPath {
		return false
	}
	task := strings.TrimSpace(claim.Task)
	if task == "" {
		task = claim.EffortID
	}
	wantFile, err := finalizeReportFileName(task, claim.Repository)
	if err != nil || filepath.Base(reportPath) != wantFile {
		return false
	}
	return strings.Contains(reportPath, filepath.Join("worklogs", claim.EffortID, "runs", claim.RunID, "reports")+string(os.PathSeparator))
}

func writeRetirementTar(files []retirementPayloadFile, destination io.Writer) error {
	tw := tar.NewWriter(destination)
	for _, file := range files {
		reader, err := openRetirementRelative(file.root, file.rel)
		if err != nil {
			return fmt.Errorf("open retirement source %s: %w", file.name, err)
		}
		info, err := reader.Stat()
		if err != nil || info.Size() != file.size {
			_ = reader.Close()
			return fmt.Errorf("retirement source changed: %s", file.name)
		}
		header := &tar.Header{Name: file.name, Mode: 0o600, Size: file.size, ModTime: time.Unix(0, 0), Format: tar.FormatPAX}
		if err := tw.WriteHeader(header); err != nil {
			_ = reader.Close()
			return err
		}
		if _, err := io.CopyN(tw, reader, file.size); err != nil {
			_ = reader.Close()
			return fmt.Errorf("read retirement source %s: %w", file.name, err)
		}
		var extra [1]byte
		if n, err := reader.Read(extra[:]); n != 0 || (err != nil && !errors.Is(err, io.EOF)) {
			_ = reader.Close()
			return fmt.Errorf("retirement source changed while reading: %s", file.name)
		}
		_ = reader.Close()
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return nil
}
