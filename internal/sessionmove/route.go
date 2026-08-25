package sessionmove

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const RouteSchemaVersion = 1
const routeFileName = "route.json"
const maxRouteBytes = 16 << 10

// Route is the immutable source-side courier address selected before the
// first delivery attempt. Resume always reuses it, so config/default changes
// cannot switch an ambiguous handoff to another courier or SSH host.
type Route struct {
	SchemaVersion int        `json:"schema_version"`
	HandoffID     string     `json:"handoff_id"`
	RequestDigest Digest     `json:"request_digest"`
	TargetMachine string     `json:"target_machine"`
	Courier       Courier    `json:"courier"`
	SSH           *SSHConfig `json:"ssh,omitempty"`
}

func (s Store) RequestBytes(handoffID string) (Request, Digest, []byte, error) {
	request, digest, raw, handoff, err := s.openRouteHandoff(handoffID)
	if err != nil {
		return Request{}, "", nil, err
	}
	_ = handoff.Close()
	return request, digest, raw, nil
}

func (s Store) SaveRoute(route Route) (Route, bool, error) {
	request, digest, _, handoff, err := s.openRouteHandoff(route.HandoffID)
	if err != nil {
		return Route{}, false, err
	}
	defer func() { _ = handoff.Close() }()
	route.SchemaVersion = RouteSchemaVersion
	if route.RequestDigest != digest || route.TargetMachine != request.TargetMachine {
		return Route{}, false, fmt.Errorf("%w: courier route does not match admitted request", ErrHandoffConflict)
	}
	if route.Courier != CourierSSH || route.SSH == nil {
		return Route{}, false, fmt.Errorf("Task 4 courier route must select configured SSH")
	}
	if err := route.SSH.Validate(); err != nil {
		return Route{}, false, err
	}
	raw, err := marshalJSON(route)
	if err != nil {
		return Route{}, false, err
	}
	created, err := publishRouteImmutableAt(handoff, raw)
	if err != nil {
		return Route{}, false, err
	}
	if created {
		return route, false, nil
	}
	existingRaw, err := readRouteFileAt(handoff, routeFileName, maxRouteBytes, "durable courier route")
	if err != nil {
		return Route{}, false, err
	}
	if !bytes.Equal(existingRaw, raw) {
		return Route{}, false, fmt.Errorf("%w: handoff %s already has a different immutable courier route", ErrHandoffConflict, route.HandoffID)
	}
	existing, err := decodeAndValidateRoute(existingRaw, request, digest)
	return existing, true, err
}

func (s Store) LoadRoute(handoffID string) (Route, error) {
	request, digest, _, handoff, err := s.openRouteHandoff(handoffID)
	if err != nil {
		return Route{}, err
	}
	defer func() { _ = handoff.Close() }()
	raw, err := readRouteFileAt(handoff, routeFileName, maxRouteBytes, "durable courier route")
	if err != nil {
		return Route{}, err
	}
	return decodeAndValidateRoute(raw, request, digest)
}

func decodeAndValidateRoute(raw []byte, request Request, digest Digest) (Route, error) {
	var route Route
	if err := decodeJSON(raw, &route); err != nil {
		return Route{}, err
	}
	if route.SchemaVersion != RouteSchemaVersion || route.HandoffID != request.HandoffID || route.RequestDigest != digest || route.TargetMachine != request.TargetMachine {
		return Route{}, fmt.Errorf("%w: durable courier route does not match admitted request", ErrHandoffConflict)
	}
	if route.Courier != CourierSSH || route.SSH == nil {
		return Route{}, errors.New("durable courier route is unsupported by this WB build")
	}
	if err := route.SSH.Validate(); err != nil {
		return Route{}, err
	}
	return route, nil
}

// openRouteHandoff retains the admitted handoff directory descriptor used for
// all subsequent route operations. Each path boundary refuses symlinks, and
// the request is read once from one bounded regular-file descriptor.
func (s Store) openRouteHandoff(handoffID string) (Request, Digest, []byte, *os.File, error) {
	if err := validateID("handoff_id", handoffID); err != nil {
		return Request{}, "", nil, nil, err
	}
	if strings.TrimSpace(s.Root) == "" || s.Root != strings.TrimSpace(s.Root) {
		return Request{}, "", nil, nil, fmt.Errorf("handoff store root is required")
	}
	rootPath, err := filepath.Abs(s.Root)
	if err != nil {
		return Request{}, "", nil, nil, fmt.Errorf("resolve handoff store root: %w", err)
	}
	rootPath = filepath.Clean(rootPath)
	rootFD, err := unix.Open(rootPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Request{}, "", nil, nil, fmt.Errorf("open handoff store root: %w", err)
	}
	root := os.NewFile(uintptr(rootFD), "wb-session-route-root")
	if root == nil {
		_ = unix.Close(rootFD)
		return Request{}, "", nil, nil, fmt.Errorf("wrap handoff store root")
	}
	handoffFD, err := unix.Openat(rootFD, handoffID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	_ = root.Close()
	if err != nil {
		return Request{}, "", nil, nil, fmt.Errorf("open handoff route directory: %w", err)
	}
	handoff := os.NewFile(uintptr(handoffFD), "wb-session-route-handoff")
	if handoff == nil {
		_ = unix.Close(handoffFD)
		return Request{}, "", nil, nil, fmt.Errorf("wrap handoff route directory")
	}
	raw, err := readRouteFileAt(handoff, requestFileName, maxExecutionLockRequestBytes, "admitted handoff request")
	if err != nil {
		_ = handoff.Close()
		return Request{}, "", nil, nil, err
	}
	request, err := DecodeRequest(raw)
	if err != nil {
		_ = handoff.Close()
		return Request{}, "", nil, nil, fmt.Errorf("decode durable handoff request: %w", err)
	}
	if request.HandoffID != handoffID {
		_ = handoff.Close()
		return Request{}, "", nil, nil, fmt.Errorf("%w: directory %s contains request %s", ErrHandoffConflict, handoffID, request.HandoffID)
	}
	return request, DigestBytes(raw), raw, handoff, nil
}

func readRouteFileAt(directory *os.File, name string, maximum int64, description string) ([]byte, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", description, err)
	}
	file := os.NewFile(uintptr(fd), "wb-session-route-"+name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap %s", description)
	}
	defer func() { _ = file.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, fmt.Errorf("inspect %s: %w", description, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size < 0 || stat.Size > maximum {
		return nil, fmt.Errorf("%s is not one single-link bounded regular file", description)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", description, err)
	}
	if int64(len(raw)) > maximum {
		return nil, fmt.Errorf("%s exceeds %d bytes", description, maximum)
	}
	if int64(len(raw)) != stat.Size {
		return nil, fmt.Errorf("%s changed while being read", description)
	}
	return raw, nil
}

func publishRouteImmutableAt(directory *os.File, raw []byte) (bool, error) {
	if len(raw) > maxRouteBytes {
		return false, fmt.Errorf("courier route exceeds %d bytes", maxRouteBytes)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return false, fmt.Errorf("name courier route temporary file: %w", err)
	}
	temporaryName := ".route-pending-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat(int(directory.Fd()), temporaryName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return false, fmt.Errorf("create courier route temporary file: %w", err)
	}
	file := os.NewFile(uintptr(fd), "wb-session-route-temporary")
	if file == nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(int(directory.Fd()), temporaryName, 0)
		return false, fmt.Errorf("wrap courier route temporary file")
	}
	temporaryExists := true
	defer func() {
		if temporaryExists {
			_ = unix.Unlinkat(int(directory.Fd()), temporaryName, 0)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("secure courier route temporary file: %w", err)
	}
	written, err := file.Write(raw)
	if err != nil || written != len(raw) {
		_ = file.Close()
		if err != nil {
			return false, fmt.Errorf("write courier route temporary file: %w", err)
		}
		return false, fmt.Errorf("write courier route temporary file: short write")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("sync courier route temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close courier route temporary file: %w", err)
	}
	if err := unix.Linkat(int(directory.Fd()), temporaryName, int(directory.Fd()), routeFileName, 0); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return false, nil
		}
		return false, fmt.Errorf("publish immutable courier route: %w", err)
	}
	if err := unix.Unlinkat(int(directory.Fd()), temporaryName, 0); err != nil {
		return false, fmt.Errorf("remove published courier route temporary link: %w", err)
	}
	temporaryExists = false
	if err := directory.Sync(); err != nil {
		return false, fmt.Errorf("sync courier route directory: %w", err)
	}
	return true, nil
}
