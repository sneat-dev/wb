package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/sessionauthority"
)

// Legacy projections retain their exact filenames and bytes. New projections
// include immutable chat scope, so successive physical actors sharing a logical
// WB identity cannot inherit another actor's terminal lifecycle.
func scopedLifecycleName(record Record, kind string) (string, error) {
	if err := validateRecordScope(record); err != nil {
		return "", err
	}
	if record.Scope == "" || !sessionauthority.ValidID(record.WBSessionID) {
		return "", fmt.Errorf("scoped lifecycle requires exact chat and WB identities")
	}
	return record.Scope + "--" + record.WBSessionID + "." + kind + ".json", nil
}

func openLifecycle(root, scope string, create bool) (*os.File, error) {
	directory, err := openRecordDirectory(root, scope, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	return openRegistryLifecycleDirectory(directory, create)
}

func readScopedLifecycle(directory *os.File, name string, value any) error {
	file, err := openRegistryLifecycleFile(directory, name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() || stat.Size() < 0 || stat.Size() > 64<<10 {
		return fmt.Errorf("scoped lifecycle marker is not one bounded private file")
	}
	if err := checkRegistryLifecyclePrivacy(file, false); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return err
	}
	if len(raw) > 64<<10 {
		return fmt.Errorf("scoped lifecycle marker exceeds size limit")
	}
	return json.Unmarshal(raw, value)
}

func recordLifecycle(root string, record Record) (parkedLifecycleMarker, bool, resumedLifecycleMarker, bool, error) {
	var p parkedLifecycleMarker
	var r resumedLifecycleMarker
	if record.Scope == "" {
		p, hasP := readParkedMarker(root, record.WBSessionID)
		r, hasR := readResumedMarker(root, record.WBSessionID)
		return p, hasP, r, hasR, nil
	}
	pName, err := scopedLifecycleName(record, "parked")
	if err != nil {
		return p, false, r, false, err
	}
	rName, _ := scopedLifecycleName(record, "resumed")
	directory, err := openLifecycle(root, record.Scope, false)
	if errors.Is(err, os.ErrNotExist) {
		return p, false, r, false, nil
	}
	if err != nil {
		return p, false, r, false, err
	}
	defer func() { _ = directory.Close() }()
	pErr := readScopedLifecycle(directory, pName, &p)
	hasP := pErr == nil
	if pErr != nil && !errors.Is(pErr, os.ErrNotExist) {
		return p, false, r, false, pErr
	}
	if hasP && (p.Scope != record.Scope || p.WBSessionID != record.WBSessionID || p.SchemaVersion != 1 || p.ParkedSessionID == "" || p.At.IsZero()) {
		return p, false, r, false, fmt.Errorf("scoped parked marker identity is invalid")
	}
	rErr := readScopedLifecycle(directory, rName, &r)
	hasR := rErr == nil
	if rErr != nil && !errors.Is(rErr, os.ErrNotExist) {
		return p, false, r, false, rErr
	}
	if hasR && (!hasP || r.Scope != record.Scope || r.WBSessionID != record.WBSessionID || r.SchemaVersion != 1 || r.ParkedSessionID != p.ParkedSessionID || r.SuccessorWBSessionID == "" || r.At.IsZero()) {
		return p, false, r, false, fmt.Errorf("scoped resumed marker identity is invalid")
	}
	return p, hasP, r, hasR, nil
}

func markScopedParked(root string, record Record, parkedID string, inj *filewrite.Injector, now func() time.Time) (Record, error) {
	name, err := scopedLifecycleName(record, "parked")
	if err != nil {
		return Record{}, err
	}
	if !sessionauthority.ValidID(parkedID) {
		return Record{}, fmt.Errorf("parked session ID is required")
	}
	p, hasP, _, hasR, err := recordLifecycle(root, record)
	if err != nil {
		return Record{}, err
	}
	if hasR || record.Lifecycle == "resumed" {
		return Record{}, fmt.Errorf("native chat session has already resumed")
	}
	if hasP {
		if p.ParkedSessionID != parkedID {
			return Record{}, fmt.Errorf("native chat session is already parked as %s", p.ParkedSessionID)
		}
	} else {
		directory, err := openLifecycle(root, record.Scope, true)
		if err != nil {
			return Record{}, err
		}
		defer func() { _ = directory.Close() }()
		marker := parkedLifecycleMarker{Scope: record.Scope, SchemaVersion: 1, WBSessionID: record.WBSessionID, ParkedSessionID: parkedID, At: now().UTC()}
		raw, err := json.MarshalIndent(marker, "", "  ")
		if err != nil {
			return Record{}, err
		}
		if err := publishRegistryLifecycle(directory, name, append(raw, '\n'), inj); err != nil {
			return Record{}, err
		}
	}
	record.Lifecycle, record.ParkedSessionID = "parked", parkedID
	return record, nil
}

func markScopedResumed(root string, record Record, parkedID, successor string, inj *filewrite.Injector, now func() time.Time) (Record, error) {
	name, err := scopedLifecycleName(record, "resumed")
	if err != nil {
		return Record{}, err
	}
	p, hasP, r, hasR, err := recordLifecycle(root, record)
	if err != nil {
		return Record{}, err
	}
	if !hasP || p.ParkedSessionID != parkedID {
		return Record{}, fmt.Errorf("native chat session is not parked as %s", parkedID)
	}
	if !sessionauthority.ValidID(successor) || successor == record.WBSessionID {
		return Record{}, fmt.Errorf("resumed session requires a distinct successor WB session ID")
	}
	if hasR {
		if r.SuccessorWBSessionID != successor {
			return Record{}, fmt.Errorf("native chat already has a different resumed projection")
		}
	} else {
		directory, err := openLifecycle(root, record.Scope, true)
		if err != nil {
			return Record{}, err
		}
		defer func() { _ = directory.Close() }()
		marker := resumedLifecycleMarker{Scope: record.Scope, SchemaVersion: 1, WBSessionID: record.WBSessionID, ParkedSessionID: parkedID, SuccessorWBSessionID: successor, At: now().UTC()}
		raw, err := json.MarshalIndent(marker, "", "  ")
		if err != nil {
			return Record{}, err
		}
		if err := publishRegistryLifecycle(directory, name, append(raw, '\n'), inj); err != nil {
			return Record{}, err
		}
	}
	record.Lifecycle, record.ParkedSessionID = "resumed", parkedID
	return record, nil
}

// RequireNoDeparture fences replay against a later park by the exact prepared
// actor. It reads its immutable record-derived namespace, never current env or
// another actor merely sharing the logical WB identity.
func RequireNoDeparture(root string, actor Record) error {
	p, hasP, r, hasR, err := recordLifecycle(root, actor)
	if err != nil {
		return err
	}
	if hasR {
		return fmt.Errorf("prepared successor departed through checkpoint %s; resume that newer checkpoint (successor %s)", r.ParkedSessionID, r.SuccessorWBSessionID)
	}
	if hasP {
		return fmt.Errorf("prepared successor is parked as newer checkpoint %s; resume that checkpoint", p.ParkedSessionID)
	}
	return nil
}

// RequireCurrentCallerAvailable fences the prepared caller as well as its fresh
// successor. The immutable source may already be parked for this exact pickup;
// its matching completion marker is also valid on an idempotent retry. Only
// lifecycle projection fields are omitted from that full source comparison.
// Callers must repeat this check under the PID barrier before acknowledgment.
func RequireCurrentCallerAvailable(root string, caller, source Record, parkedID, successorID string) error {
	p, hasP, r, hasR, err := recordLifecycle(root, caller)
	if err != nil {
		return err
	}
	if !hasP && !hasR {
		if caller.Lifecycle != "" {
			return fmt.Errorf("current caller lifecycle has no matching checkpoint evidence")
		}
		return nil
	}
	original, expected := caller, source
	original.Lifecycle, original.ParkedSessionID = "", ""
	expected.Lifecycle, expected.ParkedSessionID = "", ""
	if reflect.DeepEqual(original, expected) && hasP && p.ParkedSessionID == parkedID &&
		(!hasR || r.SuccessorWBSessionID == successorID) &&
		(caller.ParkedSessionID == "" || caller.ParkedSessionID == parkedID) &&
		(caller.Lifecycle == "" || caller.Lifecycle == "parked" || (caller.Lifecycle == "resumed" && hasR)) {
		return nil
	}
	if hasR {
		return fmt.Errorf("current caller departed through checkpoint %s; resume that newer checkpoint (successor %s)", r.ParkedSessionID, r.SuccessorWBSessionID)
	}
	return fmt.Errorf("current caller is parked as newer checkpoint %s; resume that checkpoint", p.ParkedSessionID)
}
