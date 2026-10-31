package session

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/unixcompat"
)

const scopeIndexName = ".scopes"

// ErrNativeBindingPending protects an unbound legacy PID row from both public
// and automatic registration. Only the owned launcher may bind its observed
// native thread; neither caller lineage nor root-session equality proves it.
var ErrNativeBindingPending = errors.New("existing Codex registration has no native-thread binding; wait for WB launch readiness or inspect the unbound legacy registration, then retry; no new session was allocated")

// NativeScope is an immutable, recomputable namespace, never a caller path.
// A scope is a chat identity; native process generation remains separate.
func NativeScope(runtime, nativeID string) (string, error) {
	if runtime != "codex" || nativeID == "" || strings.TrimSpace(nativeID) != nativeID || len(nativeID) > 4096 || !utf8.ValidString(nativeID) {
		return "", fmt.Errorf("Codex native scope requires an exact native chat ID")
	}
	for _, r := range nativeID {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return "", fmt.Errorf("native chat ID contains control text")
		}
	}
	digest := sha256.Sum256([]byte("wb.session.native-scope.v1\x00" + runtime + "\x00" + nativeID))
	return "codex-" + hex.EncodeToString(digest[:]), nil
}

func validScopeName(scope string) bool {
	if len(scope) != 70 || !strings.HasPrefix(scope, "codex-") {
		return false
	}
	for _, r := range scope[6:] {
		if r < '0' || r > '9' && r < 'a' || r > 'f' {
			return false
		}
	}
	return true
}

func validateRecordScope(record Record) error {
	native, err := effectiveRecordNative(record)
	if err != nil {
		return err
	}
	if record.Scope == "" {
		return nil
	}
	expected, err := NativeScope(record.Runtime, native)
	if err != nil {
		return err
	}
	if expected != record.Scope {
		return fmt.Errorf("session scope contradicts immutable runtime/native identity")
	}
	return nil
}

func effectiveRecordNative(record Record) (string, error) {
	if record.NativeHarnessID != "" && record.AgentID != "" && record.NativeHarnessID != record.AgentID {
		return "", fmt.Errorf("native harness ID %q conflicts with legacy agent ID %q", record.NativeHarnessID, record.AgentID)
	}
	if record.NativeHarnessID != "" {
		return record.NativeHarnessID, nil
	}
	return record.AgentID, nil
}

// openRecordDirectory traverses every new namespace through retained no-follow
// descriptors. A scoped record can never redirect IO through an ancestor link.
func openRecordDirectory(root, scope string, create bool) (*os.File, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, fmt.Errorf("session root must be one clean absolute path")
	}
	if scope != "" && !validScopeName(scope) {
		return nil, fmt.Errorf("invalid native session namespace")
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(fd), root)
	if scope == "" {
		return directory, nil
	}
	for _, name := range []string{scopeIndexName, scope} {
		created := false
		if create {
			err := unix.Mkdirat(int(directory.Fd()), name, 0o700)
			created = err == nil
			if err != nil && !errors.Is(err, unix.EEXIST) {
				_ = directory.Close()
				return nil, err
			}
		}
		path := filepath.Join(directory.Name(), name)
		nextFD, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = directory.Close()
		if err != nil {
			return nil, err
		}
		directory = os.NewFile(uintptr(nextFD), path)
		if created {
			if err := protectRegistryPrivate(directory); err != nil {
				_ = directory.Close()
				return nil, err
			}
		}
		if err := checkRegistryPrivacy(directory, 0o700, true); err != nil {
			_ = directory.Close()
			return nil, err
		}
	}
	return directory, nil
}

func readScopeRecord(directory *os.File, pid int, scope string) (Record, bool, error) {
	access := nativeExactRecordIO()
	access.state = func(Record) string { return StateLive }
	record, _, err := lookupExactDirectoryWithIO(directory, pid, access)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	if record.Scope != scope {
		return Record{}, false, fmt.Errorf("session payload belongs to another native namespace")
	}
	if err := validateRecordScope(record); err != nil {
		return Record{}, false, err
	}
	return record, true, nil
}

// LookupRecordExact reads only the namespace defined by immutable record
// identity. It never consults ambient native variables for a recipient/source.
func LookupRecordExact(root string, expected Record) (Record, bool, error) {
	if expected.PID <= 0 {
		return Record{}, false, fmt.Errorf("record-qualified lookup requires a positive PID")
	}
	if err := validateRecordScope(expected); err != nil {
		return Record{}, false, err
	}
	directory, err := openRecordDirectory(root, expected.Scope, false)
	if err != nil {
		return Record{}, false, err
	}
	defer func() { _ = directory.Close() }()
	record, found, err := readScopeRecord(directory, expected.PID, expected.Scope)
	if err != nil || !found {
		return Record{}, false, err
	}
	native, _ := effectiveRecordNative(expected)
	if expected.WBSessionID != "" && record.WBSessionID != expected.WBSessionID || native != "" && record.NativeHarnessID != native || expected.Runtime != "" && record.Runtime != expected.Runtime || !expected.ProcessStartedAt.IsZero() && !record.ProcessStartedAt.Equal(expected.ProcessStartedAt) {
		return Record{}, false, fmt.Errorf("record-qualified lookup found a different session identity")
	}
	processState := state(record)
	if processState == StateUnknown {
		return record, false, fmt.Errorf("recorded session process instance is unknown")
	}
	return record, processState == StateLive, nil
}

// RegisterCaller isolates public Codex declarations. The low-level Register
// used by WB's pre-native private child never reads the caller's environment.
// Same-native live legacy rows stay in place, retaining WB identity/custody.
func RegisterCaller(root string, record Record) (Record, error) {
	return registerCallerWithObservation(root, record, IsRuntimeProcess)
}

func registerCallerWithObservation(root string, record Record, observedShared func(int, string) bool) (Record, error) {
	unlock, err := lockPID(root, record.PID)
	if err != nil {
		return Record{}, err
	}
	defer unlock()
	record.Runtime = strings.TrimSpace(record.Runtime)
	record.NativeHarnessID = strings.TrimSpace(record.NativeHarnessID)
	record.AgentID = strings.TrimSpace(record.AgentID)
	if record.NativeHarnessID != "" && record.AgentID != "" && record.NativeHarnessID != record.AgentID {
		return Record{}, fmt.Errorf("native harness ID %q conflicts with legacy agent ID %q", record.NativeHarnessID, record.AgentID)
	}
	if record.NativeHarnessID == "" {
		record.NativeHarnessID = record.AgentID
	}
	shared := observedShared(record.PID, "codex")
	if shared && record.Runtime != "codex" {
		return Record{}, fmt.Errorf("declared runtime contradicts the observed shared Codex harness")
	}
	if record.Runtime == "codex" && record.NativeHarnessID != "" && (shared || record.Scope != "") {
		scope, err := NativeScope(record.Runtime, record.NativeHarnessID)
		if err != nil {
			return Record{}, err
		}
		own, ownLive, ownErr := LookupRecordExact(root, Record{PID: record.PID, Runtime: record.Runtime, NativeHarnessID: record.NativeHarnessID, Scope: scope})
		if ownErr != nil && !errors.Is(ownErr, os.ErrNotExist) {
			return Record{}, ownErr
		}
		if ownLive {
			if record.Scope != "" && record.Scope != scope {
				return Record{}, fmt.Errorf("caller supplied a different native session namespace")
			}
			if record.WBSessionID != "" && record.WBSessionID != own.WBSessionID {
				return Record{}, fmt.Errorf("live native chat already has a different WB session identity; use its current pickup transition")
			}
			record.Scope = scope
			return registerRecordUnderPIDLock(root, record)
		}
		legacyDirectory, err := openRecordDirectory(root, "", false)
		if err != nil {
			return Record{}, err
		}
		legacy, exists, err := readScopeRecord(legacyDirectory, record.PID, "")
		_ = legacyDirectory.Close()
		if err != nil {
			return Record{}, err
		}
		if exists && legacy.NativeHarnessID == "" && state(legacy) != StateGone {
			return Record{}, ErrNativeBindingPending
		}
		ownLegacy := exists && legacy.Scope == "" && legacy.Runtime == record.Runtime && legacy.NativeHarnessID == record.NativeHarnessID && state(legacy) == StateLive
		if ownLegacy {
			if record.Scope != "" {
				return Record{}, fmt.Errorf("same-native legacy session remains in its original namespace")
			}
		} else {
			if exists && legacy.NativeHarnessID != record.NativeHarnessID && record.WBSessionID != "" && record.WBSessionID == legacy.WBSessionID {
				return Record{}, fmt.Errorf("native chat cannot inherit another chat's WB identity")
			}
			if record.Scope != "" && record.Scope != scope {
				return Record{}, fmt.Errorf("caller supplied a different native session namespace")
			}
			record.Scope = scope
		}
	} else if shared {
		return Record{}, fmt.Errorf("shared Codex app-server registration requires the native chat ID")
	}
	return registerRecordUnderPIDLock(root, record)
}

func registerRecordUnderPIDLock(root string, record Record) (Record, error) {
	if err := validateRecordScope(record); err != nil {
		return Record{}, err
	}
	if record.Scope == "" {
		return registerWithMachineAndMode(root, record, os.Hostname, os.Chmod)
	}
	directory, err := openRecordDirectory(root, record.Scope, true)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = directory.Close() }()
	return registerWithProcessObservationAt(root, record, os.Hostname, os.Chmod, processAlive, processStartTime, directory)
}

// LookupSessionIdentity resolves an exact recipient independently of ambient
// chat identity. More than one live match is a conflict, never a first-row win.
func LookupSessionIdentity(root string, pid int, wbID, nativeID string) (Record, bool, error) {
	views, err := List(root)
	if err != nil {
		return Record{}, false, err
	}
	var match Record
	found := false
	for _, view := range views {
		if view.PID != pid || view.WBSessionID != wbID || view.NativeHarnessID != nativeID || view.State != StateLive {
			continue
		}
		record, live, err := LookupActiveRecordExact(root, view.Record)
		if err != nil {
			return Record{}, false, err
		}
		if !live {
			continue
		}
		if found {
			return Record{}, false, fmt.Errorf("session recipient identity is ambiguous across native namespaces")
		}
		match, found = record, true
	}
	return match, found, nil
}

func scopeRecordName(pid int) string { return strconv.Itoa(pid) + ".json" }

func listScopedRecords(root string) ([]Record, error) {
	rootDirectory, err := openRecordDirectory(root, "", false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rootDirectory.Close() }()
	fd, err := unix.Openat(int(rootDirectory.Fd()), scopeIndexName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	index := os.NewFile(uintptr(fd), filepath.Join(root, scopeIndexName))
	defer func() { _ = index.Close() }()
	if err := checkRegistryPrivacy(index, 0o700, true); err != nil {
		return nil, err
	}
	entries, err := index.ReadDir(4097)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > 4096 {
		return nil, fmt.Errorf("native session index exceeds bounded scope count")
	}
	var records []Record
	for _, entry := range entries {
		if !entry.IsDir() || !validScopeName(entry.Name()) {
			return nil, fmt.Errorf("native session index contains an invalid namespace")
		}
		// Reopening through root+index no-follow descriptors validates every
		// component again; each record read retains that exact final directory.
		directory, err := openRecordDirectory(root, entry.Name(), false)
		if err != nil {
			return nil, err
		}
		children, readErr := directory.ReadDir(4097)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			_ = directory.Close()
			return nil, readErr
		}
		if len(children) > 4096 {
			_ = directory.Close()
			return nil, fmt.Errorf("native session namespace exceeds bounded record count")
		}
		for _, child := range children {
			pid, err := strconv.Atoi(strings.TrimSuffix(child.Name(), ".json"))
			if child.IsDir() || err != nil || pid <= 0 || child.Name() != scopeRecordName(pid) {
				continue
			}
			record, found, err := readScopeRecord(directory, pid, entry.Name())
			if err != nil {
				_ = directory.Close()
				return nil, err
			}
			if found {
				records = append(records, record)
			}
		}
		_ = directory.Close()
	}
	return records, nil
}

func sessionView(root string, record Record) View {
	viewState := state(record)
	parkedMarker, hasParked, resumedMarker, hasResumed, err := recordLifecycle(root, record)
	if err != nil {
		return View{Record: record, State: StateUnknown}
	}
	if hasResumed || record.Lifecycle == "resumed" {
		record.Lifecycle = "resumed"
		if hasResumed {
			record.ParkedSessionID = resumedMarker.ParkedSessionID
		}
		viewState = StateResumed
	} else if record.Lifecycle == "parked" || hasParked {
		record.Lifecycle = "parked"
		if hasParked {
			record.ParkedSessionID = parkedMarker.ParkedSessionID
		}
		viewState = StateParked
	}
	return View{Record: record, State: viewState}
}

func pruneRecordUnderPIDLock(root string, expected Record) bool {
	if validateRecordScope(expected) != nil {
		return false
	}
	directory, err := openRecordDirectory(root, expected.Scope, false)
	if err != nil {
		return false
	}
	defer func() { _ = directory.Close() }()
	current, found, err := readScopeRecord(directory, expected.PID, expected.Scope)
	if err != nil || !found || current.WBSessionID != expected.WBSessionID || !current.ProcessStartedAt.Equal(expected.ProcessStartedAt) || state(current) != StateGone {
		return false
	}
	return unix.Unlinkat(int(directory.Fd()), scopeRecordName(expected.PID), 0) == nil
}

// CallerNativeID is used only at invocation boundaries. CODEX_THREAD_ID is the
// per-chat identity; CODEX_SESSION_ID is root context shared by descendant
// threads and never an ownership key. Registered NativeHarnessID/AgentID remain
// aliases of the per-chat ID. Recipient/source reads use the recorded namespace.
func CallerNativeID(runtime string, lookup func(string) (string, bool)) (string, error) {
	if runtime != "codex" {
		value, _ := lookup("CLAUDE_CODE_SESSION_ID")
		return value, nil
	}
	thread, _ := lookup("CODEX_THREAD_ID")
	root, _ := lookup("CODEX_SESSION_ID")
	if root != "" {
		if _, err := NativeScope(runtime, root); err != nil {
			return "", fmt.Errorf("invalid Codex root context: %w", err)
		}
	}
	if thread == "" {
		if root != "" {
			return "", fmt.Errorf("CODEX_THREAD_ID is required: CODEX_SESSION_ID is shared root context, not this chat's identity")
		}
		return "", nil
	}
	if _, err := NativeScope(runtime, thread); err != nil {
		return "", err
	}
	return thread, nil
}

func lookupCallerNative(root string, pid int, runtime, native string) (Record, bool, error) {
	var scoped Record
	var scopedLive bool
	if runtime == "codex" {
		scope, err := NativeScope(runtime, native)
		if err != nil {
			return Record{}, false, err
		}
		scoped, scopedLive, err = LookupRecordExact(root, Record{PID: pid, Runtime: runtime, NativeHarnessID: native, Scope: scope})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Record{}, false, err
		}
	}
	legacy, legacyLive, err := LookupRecordExact(root, Record{PID: pid})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Record{}, false, err
	}
	ownLegacy := legacy.PID > 0 && legacy.NativeHarnessID == native
	if ownLegacy && legacy.Runtime != runtime {
		return Record{}, false, fmt.Errorf("registered caller runtime differs from observed harness")
	}
	if scoped.PID > 0 && ownLegacy && scopedLive && legacyLive && scoped.WBSessionID == legacy.WBSessionID {
		return Record{}, false, fmt.Errorf("native chat has duplicate live WB identity across legacy and scoped records")
	}
	if scoped.PID > 0 {
		return scoped, scopedLive, nil
	}
	if ownLegacy {
		return legacy, legacyLive, nil
	}
	return Record{}, false, nil
}

// ValidateScope authenticates an optional namespace without changing legacy
// canonical records or consulting process/environment state.
func (r Record) ValidateScope() error { return validateRecordScope(r) }

func writeRecordAtomic(root string, record Record, raw []byte) error {
	if err := validateRecordScope(record); err != nil {
		return err
	}
	directory, err := openRecordDirectory(root, record.Scope, false)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return filewrite.WriteBytesAtomicAtExactMode(directory, scopeRecordName(record.PID), raw, 0o644)
}

// MarkRecordParked binds the caller's namespace before appending the existing
// namespace-qualified lifecycle marker. Other chats sharing its PID are untouched.
func MarkRecordParked(root string, expected Record, parkedID string) (Record, error) {
	unlock, err := lockPID(root, expected.PID)
	if err != nil {
		return Record{}, err
	}
	defer unlock()
	record, live, err := LookupRecordExact(root, expected)
	if err != nil {
		return Record{}, err
	}
	if !live {
		return Record{}, fmt.Errorf("parked source is not the exact live native process generation")
	}
	return markParkedRecordAt(root, record, parkedID, nil, time.Now)
}

// LookupCallerIdentity resolves one explicitly named chat at an invocation
// boundary, retaining lifecycle projection so claim auto-registration refuses
// to overwrite its own parked history.
func LookupCallerIdentity(root string, pid int, runtime, native string) (Record, bool, error) {
	if native == "" {
		return Record{}, false, fmt.Errorf("caller native chat identity is unavailable")
	}
	record, live, err := lookupCallerNative(root, pid, runtime, native)
	if err != nil || record.PID == 0 {
		return record, live, err
	}
	view := sessionView(root, record)
	if view.State == StateUnknown {
		return record, false, fmt.Errorf("caller lifecycle is uncertain")
	}
	return view.Record, live && view.State == StateLive, nil
}

// LookupActiveRecordExact adds the record-qualified lifecycle projection to
// physical generation proof. Custody consumers must not treat a still-running
// but parked source as an active logical owner.
func LookupActiveRecordExact(root string, expected Record) (Record, bool, error) {
	record, live, err := LookupRecordExact(root, expected)
	if err != nil || record.PID == 0 {
		return record, live, err
	}
	view := sessionView(root, record)
	if view.State == StateUnknown {
		return view.Record, false, fmt.Errorf("exact session lifecycle is uncertain")
	}
	return view.Record, live && view.State == StateLive, nil
}
