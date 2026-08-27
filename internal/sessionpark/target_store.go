package sessionpark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"golang.org/x/sys/unix"
)

const (
	targetReceiptFileName = "receipt.json"
	targetEventsDirName   = "events"
	targetLockFileName    = "receive.lock"
)

type TargetStore struct{ Root string }

type TargetAdmission struct {
	Envelope Envelope
	Digest   sessionmove.Digest
	Replay   bool
	Receipt  *Receipt
}

type TargetEvent struct {
	SchemaVersion int       `json:"schema_version"`
	Sequence      uint64    `json:"sequence"`
	ResumeID      string    `json:"resume_id"`
	Phase         string    `json:"phase"`
	At            time.Time `json:"at"`
}

type TargetLock struct {
	mu           sync.Mutex
	root         *os.File
	aggregate    *os.File
	envelopeFile *os.File
	file         *os.File
	rootPath     string
	resumeID     string
	digest       sessionmove.Digest
	envelope     Envelope
}

func NewTargetStore(root string) TargetStore { return TargetStore{Root: root} }

func (store TargetStore) Admit(raw []byte) (TargetAdmission, error) {
	envelope, err := DecodeEnvelope(raw)
	if err != nil {
		return TargetAdmission{}, err
	}
	canonical, err := EncodeEnvelope(envelope)
	if err != nil {
		return TargetAdmission{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return TargetAdmission{}, fmt.Errorf("park resume envelope must use WB's canonical JSON encoding")
	}
	digest := sessionmove.DigestBytes(raw)
	root, err := cleanAbsoluteStoreRoot(store.Root)
	if err != nil {
		return TargetAdmission{}, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return TargetAdmission{}, err
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return TargetAdmission{}, fmt.Errorf("open private park resume store root: %w", err)
	}
	rootDir := os.NewFile(uintptr(rootFD), "wb-park-resume-admit-root")
	defer func() { _ = rootDir.Close() }()
	if err := unix.Fchmod(rootFD, 0o700); err != nil {
		return TargetAdmission{}, err
	}
	admitName := ".admit-" + envelope.Request.ResumeID + ".lock"
	admitFD, err := openOrCreateRegularAt(rootFD, admitName, 0o600)
	if err != nil {
		return TargetAdmission{}, err
	}
	admit := os.NewFile(uintptr(admitFD), "wb-park-resume-admit-lock")
	defer func() { _ = admit.Close() }()
	if err := unix.Flock(int(admit.Fd()), unix.LOCK_EX); err != nil {
		return TargetAdmission{}, err
	}
	defer func() { _ = unix.Flock(int(admit.Fd()), unix.LOCK_UN) }()
	created := false
	if err := unix.Mkdirat(rootFD, envelope.Request.ResumeID, 0o700); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return TargetAdmission{}, fmt.Errorf("create park resume target aggregate: %w", err)
		}
	} else {
		created = true
	}
	aggregateFD, err := unix.Openat(rootFD, envelope.Request.ResumeID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return TargetAdmission{}, fmt.Errorf("open private park resume target aggregate: %w", err)
	}
	aggregate := os.NewFile(uintptr(aggregateFD), "wb-park-resume-admit-aggregate")
	defer func() { _ = aggregate.Close() }()
	if err := unix.Fchmod(aggregateFD, 0o700); err != nil {
		return TargetAdmission{}, err
	}
	if err := writeExactPrivateAt(aggregate, EnvelopeFileName, raw); err != nil {
		return TargetAdmission{}, fmt.Errorf("persist exact park resume envelope: %w", err)
	}
	if err := writeExactPrivateAt(aggregate, ContinuationFileName, []byte(envelope.Request.Continuation)); err != nil {
		return TargetAdmission{}, fmt.Errorf("persist private park resume continuation: %w", err)
	}
	if err := unix.Mkdirat(aggregateFD, targetEventsDirName, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return TargetAdmission{}, err
	}
	eventsFD, err := unix.Openat(aggregateFD, targetEventsDirName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return TargetAdmission{}, err
	}
	events := os.NewFile(uintptr(eventsFD), "wb-park-resume-admit-events")
	if err := unix.Fchmod(eventsFD, 0o700); err != nil {
		_ = events.Close()
		return TargetAdmission{}, err
	}
	if err := events.Sync(); err != nil {
		_ = events.Close()
		return TargetAdmission{}, err
	}
	_ = events.Close()
	if err := aggregate.Sync(); err != nil {
		return TargetAdmission{}, err
	}
	if err := rootDir.Sync(); err != nil {
		return TargetAdmission{}, err
	}
	receipt, err := loadReceiptAt(aggregate)
	if err != nil {
		return TargetAdmission{}, err
	}
	if receipt != nil {
		if err := ValidateReceipt(*receipt, envelope.Request, digest); err != nil {
			return TargetAdmission{}, err
		}
	}
	return TargetAdmission{Envelope: envelope, Digest: digest, Replay: !created, Receipt: receipt}, nil
}

func (store TargetStore) Acquire(ctx context.Context, resumeID string, digest sessionmove.Digest) (*TargetLock, error) {
	if !strings.HasPrefix(resumeID, "resume-") || !strings.HasPrefix(string(digest), sessionmove.DigestAlgorithmSHA256+":") {
		return nil, fmt.Errorf("park resume target authority identity is invalid")
	}
	rootPath, err := cleanAbsoluteStoreRoot(store.Root)
	if err != nil {
		return nil, err
	}
	rootFD, err := unix.Open(rootPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open park resume store root: %w", err)
	}
	root := os.NewFile(uintptr(rootFD), "wb-park-resume-store")
	aggregateFD, err := unix.Openat(rootFD, resumeID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("open park resume aggregate: %w", err)
	}
	aggregate := os.NewFile(uintptr(aggregateFD), "wb-park-resume-aggregate")
	envelopeFD, err := unix.Openat(aggregateFD, EnvelopeFileName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = aggregate.Close()
		_ = root.Close()
		return nil, fmt.Errorf("open admitted park resume envelope: %w", err)
	}
	envelopeFile := os.NewFile(uintptr(envelopeFD), "wb-park-resume-envelope")
	raw, err := readBoundedRegular(envelopeFile, MaxEnvelopeBytes)
	if err != nil || !digest.Matches(raw) {
		_ = envelopeFile.Close()
		_ = aggregate.Close()
		_ = root.Close()
		return nil, fmt.Errorf("admitted park resume envelope does not match exact digest")
	}
	envelope, err := DecodeEnvelope(raw)
	if err != nil || envelope.Request.ResumeID != resumeID {
		_ = envelopeFile.Close()
		_ = aggregate.Close()
		_ = root.Close()
		return nil, fmt.Errorf("admitted park resume envelope identity conflicts with aggregate")
	}
	lockFD, err := openTargetLock(aggregateFD)
	if err != nil {
		_ = envelopeFile.Close()
		_ = aggregate.Close()
		_ = root.Close()
		return nil, err
	}
	file := os.NewFile(uintptr(lockFD), "wb-park-resume-lock")
	for {
		if err := unix.Flock(lockFD, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return &TargetLock{root: root, aggregate: aggregate, envelopeFile: envelopeFile, file: file,
				rootPath: rootPath, resumeID: resumeID, digest: digest, envelope: envelope}, nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = file.Close()
			_ = envelopeFile.Close()
			_ = aggregate.Close()
			_ = root.Close()
			return nil, fmt.Errorf("lock park resume execution: %w", err)
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = file.Close()
			_ = envelopeFile.Close()
			_ = aggregate.Close()
			_ = root.Close()
			return nil, fmt.Errorf("wait for park resume execution lock: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func (lock *TargetLock) HeldForSession(expectedRoot, aggregateID, digest string) bool {
	if lock == nil {
		return false
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	return lock.heldForSessionLocked(expectedRoot, aggregateID, digest)
}

func (lock *TargetLock) heldForSessionLocked(expectedRoot, aggregateID, digest string) bool {
	if lock.root == nil || lock.aggregate == nil || lock.envelopeFile == nil || lock.file == nil ||
		lock.resumeID != aggregateID || string(lock.digest) != digest {
		return false
	}
	rootPath, err := filepath.Abs(expectedRoot)
	if err != nil || filepath.Clean(rootPath) != lock.rootPath {
		return false
	}
	rootFD, err := unix.Open(rootPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	root := os.NewFile(uintptr(rootFD), "wb-park-resume-root-check")
	defer func() { _ = root.Close() }()
	if !sameFile(lock.root, root) {
		return false
	}
	aggregateFD, err := unix.Openat(rootFD, aggregateID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	aggregate := os.NewFile(uintptr(aggregateFD), "wb-park-resume-aggregate-check")
	defer func() { _ = aggregate.Close() }()
	if !sameFile(lock.aggregate, aggregate) {
		return false
	}
	envelopeFD, err := unix.Openat(aggregateFD, EnvelopeFileName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	envelope := os.NewFile(uintptr(envelopeFD), "wb-park-resume-envelope-check")
	defer func() { _ = envelope.Close() }()
	if !sameFile(lock.envelopeFile, envelope) {
		return false
	}
	raw, err := readBoundedRegular(envelope, MaxEnvelopeBytes)
	if err != nil || !lock.digest.Matches(raw) {
		return false
	}
	lockFD, err := unix.Openat(aggregateFD, targetLockFileName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	file := os.NewFile(uintptr(lockFD), "wb-park-resume-lock-check")
	defer func() { _ = file.Close() }()
	return sameFile(lock.file, file)
}

func (lock *TargetLock) RetainSessionDir(expectedRoot, aggregateID, digest string) (*os.File, error) {
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if !lock.heldForSessionLocked(expectedRoot, aggregateID, digest) {
		return nil, fmt.Errorf("park resume lock does not retain the exact admitted aggregate")
	}
	fd, err := unix.Dup(int(lock.aggregate.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), "wb-park-resume-retained-aggregate"), nil
}

func (lock *TargetLock) Envelope() Envelope { return lock.envelope }

func (lock *TargetLock) Close() error {
	if lock == nil {
		return nil
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	var errs []error
	if lock.file != nil {
		errs = append(errs, unix.Flock(int(lock.file.Fd()), unix.LOCK_UN), lock.file.Close())
	}
	for _, file := range []*os.File{lock.envelopeFile, lock.aggregate, lock.root} {
		if file != nil {
			errs = append(errs, file.Close())
		}
	}
	lock.file, lock.envelopeFile, lock.aggregate, lock.root = nil, nil, nil, nil
	return errors.Join(errs...)
}

func (store TargetStore) LoadReceiptUnderLock(lock *TargetLock, request RemoteRequest, digest sessionmove.Digest) (*Receipt, error) {
	if lock == nil || !lock.HeldForSession(store.Root, request.ResumeID, string(digest)) {
		return nil, fmt.Errorf("load receipt requires exact admitted park resume authority")
	}
	receipt, err := loadReceiptAt(lock.aggregate)
	if err != nil || receipt == nil {
		return receipt, err
	}
	if err := ValidateReceipt(*receipt, request, digest); err != nil {
		return nil, err
	}
	return receipt, nil
}

// EnsureSuccessorContextUnderLock publishes the one private continuation read
// by the launched harness. It contains the bounded source continuation plus
// every deterministic target member path, pin, and target Work Log reference;
// neither the bytes nor the path are returned by the public receiver result.
func (store TargetStore) EnsureSuccessorContextUnderLock(lock *TargetLock, request RemoteRequest, digest sessionmove.Digest, members []ReceiptMember) (string, []byte, error) {
	if lock == nil || !lock.HeldForSession(store.Root, request.ResumeID, string(digest)) {
		return "", nil, fmt.Errorf("publish successor context requires exact admitted park resume authority")
	}
	if len(members) != len(request.Members) || len(members) == 0 {
		return "", nil, fmt.Errorf("successor context requires every admitted member")
	}
	var body strings.Builder
	body.WriteString(request.Continuation)
	if !strings.HasSuffix(request.Continuation, "\n") {
		body.WriteByte('\n')
	}
	body.WriteString("\nTarget worktrees:\n")
	for index, member := range members {
		admitted := request.Members[index]
		if member.MemberID != admitted.MemberID || member.Repository != admitted.Repository || member.Commit != admitted.Commit ||
			member.Pin != MemberPin(request.ResumeID, admitted.MemberID) {
			return "", nil, fmt.Errorf("successor context member %d conflicts with admitted request", index)
		}
		wantReference, err := TargetWorkLogReference(request, digest, admitted)
		if err != nil || member.TargetWorkLogReference != wantReference || !filepath.IsAbs(member.TargetPath) || filepath.Clean(member.TargetPath) != member.TargetPath {
			return "", nil, fmt.Errorf("successor context member %s lacks corroborated target identity", admitted.MemberID)
		}
		fmt.Fprintf(&body, "- %s %s\n  path: %s\n  pin: %s\n  commit: %s\n  work_log: %s\n",
			member.MemberID, member.Repository, member.TargetPath, member.Pin, member.Commit, member.TargetWorkLogReference)
	}
	raw := []byte(body.String())
	if len(raw) > MaxSuccessorContextBytes {
		return "", nil, fmt.Errorf("private successor context exceeds %d bytes", MaxSuccessorContextBytes)
	}
	if err := writeExactPrivateAt(lock.aggregate, SuccessorContextFileName, raw); err != nil {
		return "", nil, err
	}
	return filepath.Join(store.Root, request.ResumeID, SuccessorContextFileName), raw, nil
}

func (store TargetStore) SaveReceiptUnderLock(lock *TargetLock, request RemoteRequest, digest sessionmove.Digest, receipt Receipt) (Receipt, bool, error) {
	if lock == nil || !lock.HeldForSession(store.Root, request.ResumeID, string(digest)) {
		return Receipt{}, false, fmt.Errorf("save receipt requires exact admitted park resume authority")
	}
	if err := ValidateReceipt(receipt, request, digest); err != nil {
		return Receipt{}, false, err
	}
	raw, err := EncodeReceipt(receipt)
	if err != nil {
		return Receipt{}, false, err
	}
	created, err := writeImmutableAt(lock.aggregate, targetReceiptFileName, raw, 0o600)
	if err != nil {
		return Receipt{}, false, err
	}
	existing, err := loadReceiptAt(lock.aggregate)
	if err != nil || existing == nil {
		return Receipt{}, false, fmt.Errorf("load durable park resume receipt after publication: %w", err)
	}
	existingRaw, encodeErr := EncodeReceipt(*existing)
	if encodeErr != nil || !bytes.Equal(existingRaw, raw) {
		return Receipt{}, false, fmt.Errorf("park resume already has a conflicting durable receipt")
	}
	return *existing, !created, nil
}

func (store TargetStore) AppendEventUnderLock(lock *TargetLock, request RemoteRequest, digest sessionmove.Digest, phase string, at time.Time) (TargetEvent, error) {
	if lock == nil || !lock.HeldForSession(store.Root, request.ResumeID, string(digest)) {
		return TargetEvent{}, fmt.Errorf("append event requires exact admitted park resume authority")
	}
	eventsFD, err := unix.Openat(int(lock.aggregate.Fd()), targetEventsDirName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return TargetEvent{}, err
	}
	events := os.NewFile(uintptr(eventsFD), "wb-park-resume-events")
	defer func() { _ = events.Close() }()
	history, err := listTargetEventsAt(events, request.ResumeID)
	if err != nil {
		return TargetEvent{}, err
	}
	for _, existing := range history {
		if existing.Phase == phase {
			return existing, nil
		}
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	event := TargetEvent{SchemaVersion: 1, Sequence: uint64(len(history) + 1), ResumeID: request.ResumeID, Phase: phase, At: at.UTC()}
	raw, _ := jsonMarshal(event)
	name := fmt.Sprintf("%020d.json", event.Sequence)
	if _, err := writeImmutableAt(events, name, raw, 0o600); err != nil {
		return TargetEvent{}, err
	}
	return event, nil
}

func (store TargetStore) EventsUnderLock(lock *TargetLock, request RemoteRequest, digest sessionmove.Digest) ([]TargetEvent, error) {
	if lock == nil || !lock.HeldForSession(store.Root, request.ResumeID, string(digest)) {
		return nil, fmt.Errorf("read events requires exact admitted park resume authority")
	}
	eventsFD, err := unix.Openat(int(lock.aggregate.Fd()), targetEventsDirName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	events := os.NewFile(uintptr(eventsFD), "wb-park-resume-events")
	defer func() { _ = events.Close() }()
	return listTargetEventsAt(events, request.ResumeID)
}

func cleanAbsoluteStoreRoot(root string) (string, error) {
	if root == "" || root != strings.TrimSpace(root) {
		return "", fmt.Errorf("park resume store root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil || filepath.Clean(absolute) != absolute || absolute != root {
		return "", fmt.Errorf("park resume store root must be clean and absolute")
	}
	return absolute, nil
}

func openTargetLock(aggregateFD int) (int, error) {
	const flags = unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := unix.Openat(aggregateFD, targetLockFileName, flags, 0)
	if err == nil {
		return fd, nil
	}
	if !errors.Is(err, unix.ENOENT) {
		return -1, err
	}
	fd, err = unix.Openat(aggregateFD, targetLockFileName, flags|unix.O_CREAT|unix.O_EXCL, 0o600)
	if errors.Is(err, unix.EEXIST) {
		return unix.Openat(aggregateFD, targetLockFileName, flags, 0)
	}
	return fd, err
}

func sameFile(first, second *os.File) bool {
	firstInfo, firstErr := first.Stat()
	secondInfo, secondErr := second.Stat()
	return firstErr == nil && secondErr == nil && os.SameFile(firstInfo, secondInfo)
}

func readBoundedRegular(file *os.File, maximum int64) ([]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		return nil, fmt.Errorf("artifact is not one bounded regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(raw)) != info.Size() {
		return nil, fmt.Errorf("artifact changed while being read")
	}
	return raw, nil
}

func readRegularAt(directory *os.File, name string, maximum int64) ([]byte, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { _ = file.Close() }()
	return readBoundedRegular(file, maximum)
}

func writeImmutableAt(directory *os.File, name string, raw []byte, mode os.FileMode) (bool, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode))
	if errors.Is(err, unix.EEXIST) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	file := os.NewFile(uintptr(fd), name)
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return false, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return false, err
	}
	return true, file.Close()
}

func writeExactPrivateAt(directory *os.File, name string, raw []byte) error {
	created, err := writeImmutableAt(directory, name, raw, 0o600)
	if err != nil {
		return err
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { _ = file.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 || stat.Nlink != 1 {
		return fmt.Errorf("private artifact %q is not one 0600 regular file", name)
	}
	existing, err := readBoundedRegular(file, MaxEnvelopeBytes)
	if err != nil || !bytes.Equal(existing, raw) {
		return fmt.Errorf("immutable private artifact %q conflicts with admitted bytes", name)
	}
	if created {
		return directory.Sync()
	}
	return nil
}

func openOrCreateRegularAt(directoryFD int, name string, mode uint32) (int, error) {
	const flags = unix.O_RDWR | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Openat(directoryFD, name, flags, 0)
	if errors.Is(err, unix.ENOENT) {
		fd, err = unix.Openat(directoryFD, name, flags|unix.O_CREAT|unix.O_EXCL, mode)
		if errors.Is(err, unix.EEXIST) {
			fd, err = unix.Openat(directoryFD, name, flags, 0)
		}
	}
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("%q is not one regular file", name)
	}
	if err := unix.Fchmod(fd, mode); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

var targetEventName = regexp.MustCompile(`^[0-9]{20}\.json$`)

func listTargetEventsAt(events *os.File, resumeID string) ([]TargetEvent, error) {
	if _, err := events.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	entries, err := events.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := make([]TargetEvent, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !targetEventName.MatchString(entry.Name()) {
			return nil, fmt.Errorf("unexpected park resume event artifact %q", entry.Name())
		}
		raw, err := readRegularAt(events, entry.Name(), 64<<10)
		if err != nil {
			return nil, err
		}
		var event TargetEvent
		sequence := uint64(len(result) + 1)
		if err := strictDecode(raw, &event); err != nil || event.SchemaVersion != 1 || event.ResumeID != resumeID ||
			event.Sequence != sequence || entry.Name() != fmt.Sprintf("%020d.json", sequence) ||
			strings.TrimSpace(event.Phase) == "" || event.At.IsZero() {
			return nil, fmt.Errorf("invalid park resume event history at %q", entry.Name())
		}
		result = append(result, event)
	}
	return result, nil
}

func loadReceiptAt(aggregate *os.File) (*Receipt, error) {
	raw, err := readRegularAt(aggregate, targetReceiptFileName, MaxEnvelopeBytes)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	receipt, err := DecodeReceipt(raw)
	return &receipt, err
}

func jsonMarshal(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	return append(raw, '\n'), err
}
