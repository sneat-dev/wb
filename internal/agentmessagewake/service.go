// Package agentmessagewake wakes explicitly bound Codex threads after durable WB delivery.
// A wake receipt means input was accepted, never that the agent processed the inbox.
package agentmessagewake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/sneat-dev/wb/internal/agentmessage"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/secureopen"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/worktreesecure"
)

const (
	NotBound  = "not-bound"
	Submitted = "submitted"
	// Rejected records an explicit server refusal. Repeating the same message
	// preserves this terminal result; a deliberate new attempt needs a new ID.
	Rejected        = "rejected"
	Ambiguous       = "ambiguous"
	PreflightFailed = "preflight-failed"
)

// Service stores receiver-local opt-in bindings and durable wake fences beneath Root.
// Root must be an absolute private directory; no remote message chooses a socket or thread.
type Service struct {
	Root          string
	ProjectsRoot  string
	dial          func(context.Context, string) (connection, error)
	chmod         func(*os.File, os.FileMode) error
	write         func(*os.File, string, any, bool) error
	syncDirectory func(*os.File) error
	syncParent    func(*os.File) error
}

type binding struct {
	SocketPath string `json:"socket_path"`
	ThreadID   string `json:"thread_id"`
}

type receipt struct {
	Digest  string  `json:"digest"`
	Binding binding `json:"binding"`
	Status  string  `json:"status"`
}

var threadPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func (b binding) validate() error {
	if !filepath.IsAbs(b.SocketPath) || len(b.SocketPath) > 4096 || strings.ContainsRune(b.SocketPath, 0) || !threadPattern.MatchString(b.ThreadID) {
		return errors.New("codex binding requires an absolute socket path and an exact thread ID")
	}
	return nil
}

// privateOpener creates newly missing ancestors with the inbox's privacy mode.
// Existing ancestors are opened without changing their permissions.
type privateOpener struct {
	secureopen.Real
	sync func(int) error
}

func (opener privateOpener) Mkdir(fd int, name string) error {
	err := unix.Mkdirat(fd, name, 0700)
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	sync := opener.sync
	if sync == nil {
		sync = unix.Fsync
	}
	if syncErr := sync(fd); syncErr != nil {
		// Existing external ancestors may be read-only or unable to sync
		// directories. Never apply this exception to a newly published child.
		if errors.Is(err, unix.EEXIST) && (errors.Is(syncErr, syscall.EINVAL) || errors.Is(syncErr, syscall.ENOTSUP) || errors.Is(syncErr, syscall.EROFS)) {
			return err
		}
		return syncErr
	}
	return err
}

func (s Service) directory(agent string) (*os.File, error) {
	if err := agentmessage.ValidateAgent(agent); err != nil {
		return nil, err
	}
	root, err := worktreesecure.OpenAbsoluteDirectoryNoFollowWith(privateOpener{}, s.Root, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	chmod := s.chmod
	if chmod == nil {
		chmod = (*os.File).Chmod
	}
	if err = chmod(root, 0700); err != nil {
		return nil, err
	}
	child, err := worktreesecure.OpenPrivateChildWith(privateOpener{}, root, agent, true, func(name string) bool { return agentmessage.ValidateAgent(name) == nil })
	if err != nil {
		return nil, err
	}
	syncParent := s.syncParent
	if syncParent == nil {
		syncParent = (*os.File).Sync
	}
	if err = syncParent(root); err != nil {
		_ = child.Close()
		return nil, err
	}
	return child, nil
}

func (s Service) sync(dir *os.File) error {
	if s.syncDirectory != nil {
		return s.syncDirectory(dir)
	}
	return dir.Sync()
}

func (s Service) persist(dir *os.File, name string, value any, idempotent bool) error {
	var err error
	if s.write != nil {
		err = s.write(dir, name, value, idempotent)
	} else {
		err = filewrite.WriteJSONImmutableAt(dir, name, value, idempotent, nil)
	}
	if err != nil {
		return err
	}
	// The immutable writer's identical-content fast path does not sync the
	// directory. Re-establish durability for both new writes and replays.
	return s.sync(dir)
}

// Bind verifies an existing exact thread through metadata-only reads before opting
// the local alias into wake-up. A different target requires an explicit Unbind.
func (s Service) Bind(ctx context.Context, agent, socketPath, threadID string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	b := binding{SocketPath: socketPath, ThreadID: threadID}
	if err := b.validate(); err != nil {
		return err
	}
	dir, err := s.directory(agent)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	conn, err := s.preflight(ctx, b)
	if err != nil {
		return err
	}
	defer conn.Close()
	return s.persist(dir, "binding.json", b, true)
}

// Unbind prevents later deliveries from starting a new wake. It does not cancel
// an already started submission and keeps fences so rebinding never replays it.
func (s Service) Unbind(agent string) error {
	dir, err := s.directory(agent)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	err = unix.Unlinkat(int(dir.Fd()), "binding.json", 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.sync(dir)
}

// Notify is suitable for agentmessage.Store.Notify. Delivery must be durable first.
// The immutable intent is synced before turn/start. An interrupted or uncertain
// submission is never retried automatically, even after rebinding the alias.
// Explicit server rejections are also terminal and retain a distinct receipt.
func (s Service) Notify(ctx context.Context, msg agentmessage.Message) (string, error) {
	if err := msg.Validate(); err != nil {
		return PreflightFailed, err
	}
	dir, err := s.directory(msg.To)
	if err != nil {
		return PreflightFailed, err
	}
	defer func() { _ = dir.Close() }()
	digest := msg.Digest()
	for _, name := range []string{msg.ID + ".result.json", msg.ID + ".intent.json"} {
		var old receipt
		err = filewrite.ReadJSONAt(dir, name, &old)
		if err == nil {
			if old.Digest != digest || (old.Status != Submitted && old.Status != Ambiguous && old.Status != Rejected) {
				return Ambiguous, errors.New("wake receipt conflicts with message")
			}
			if err = s.sync(dir); err != nil {
				return Ambiguous, errors.New("wake receipt directory could not be synced; no new submission attempted")
			}
			return old.Status, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return Ambiguous, errors.New("cannot read wake receipt")
		}
	}
	var b binding
	if err = filewrite.ReadJSONAt(dir, "binding.json", &b); errors.Is(err, os.ErrNotExist) {
		return NotBound, nil
	}
	if err != nil {
		return PreflightFailed, errors.New("cannot read Codex binding")
	}
	if err = b.validate(); err != nil {
		return PreflightFailed, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := s.preflight(ctx, b)
	if err != nil {
		return PreflightFailed, err
	}
	defer conn.Close()
	record := receipt{Digest: digest, Binding: b, Status: Ambiguous}
	if err = s.persist(dir, msg.ID+".intent.json", record, false); err != nil {
		// A definite absence proves this attempt did not publish its fence and
		// cannot have submitted input. A competitor or unreadable entry remains
		// ambiguous; neither case is permission to send.
		if _, readErr := filewrite.ReadAt(dir, msg.ID+".intent.json"); errors.Is(readErr, os.ErrNotExist) {
			return PreflightFailed, errors.New("wake intent was not recorded; no submission attempted")
		}
		return Ambiguous, errors.New("wake intent could not be exclusively recorded; no new submission attempted")
	}
	command := "wb"
	if s.ProjectsRoot != "" {
		command += " --projects-root '" + strings.ReplaceAll(s.ProjectsRoot, "'", "'\"'\"'") + "'"
	}
	prompt := fmt.Sprintf("New message from %s.\n%s message receive --agent %s", msg.From, command, msg.To)
	params := map[string]any{"threadId": b.ThreadID, "clientUserMessageId": msg.ID, "input": []any{map[string]any{"type": "text", "text": prompt}}}
	raw, err := conn.Call("turn/start", params)
	if err != nil {
		var rejection *rpcRejectionError
		if !errors.As(err, &rejection) {
			return Ambiguous, errors.New("codex wake outcome is uncertain; automatic retry is disabled")
		}
		record.Status = Rejected
		if err = s.persist(dir, msg.ID+".result.json", record, true); err != nil {
			return Ambiguous, errors.New("codex rejected the wake but its receipt could not be persisted")
		}
		return Rejected, rejection
	}
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Turn.ID == "" {
		return Ambiguous, errors.New("codex returned no valid turn receipt; automatic retry is disabled")
	}
	record.Status = Submitted
	if err = s.persist(dir, msg.ID+".result.json", record, true); err != nil {
		return Ambiguous, errors.New("codex accepted the wake but its receipt could not be persisted")
	}
	return Submitted, nil
}

func (s Service) preflight(ctx context.Context, b binding) (connection, error) {
	dial := s.dial
	if dial == nil {
		dial = dialCodex
	}
	conn, err := dial(ctx, b.SocketPath)
	if err != nil {
		return nil, errors.New("cannot connect to the configured Codex server")
	}
	if _, err = conn.Call("initialize", map[string]any{"clientInfo": map[string]string{"name": "wb-agent-message", "version": "1"}}); err != nil {
		conn.Close()
		return nil, errors.New("codex initialization failed")
	}
	if err = conn.Notify("initialized"); err != nil {
		conn.Close()
		return nil, errors.New("codex initialization notification failed")
	}
	raw, err := conn.Call("thread/read", map[string]any{"threadId": b.ThreadID, "includeTurns": false})
	var read struct {
		Thread struct {
			ID     string `json:"id"`
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"thread"`
	}
	if err != nil || json.Unmarshal(raw, &read) != nil || read.Thread.ID != b.ThreadID || (read.Thread.Status.Type != "idle" && read.Thread.Status.Type != "active") {
		conn.Close()
		return nil, errors.New("configured Codex thread is not available or does not match")
	}
	return conn, nil
}
