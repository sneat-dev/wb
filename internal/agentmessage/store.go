package agentmessage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/secureopen"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/worktreesecure"
)

// Store uses an owner-only directory on the receiving machine. Notify is an
// optional harness adapter: it must independently fence ambiguous submissions.
type Store struct {
	Root          string
	Notify        func(context.Context, Message) (string, error)
	syncDirectory func(*os.File) error
}

type privateOpener struct{ secureopen.Real }

func (privateOpener) Mkdir(fd int, name string) error {
	return mkdirPrivate(fd, name, unix.Mkdirat, unix.Fsync)
}

func mkdirPrivate(fd int, name string, mkdir func(int, string, uint32) error, sync func(int) error) error {
	err := mkdir(fd, name, 0700)
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return err
	}
	// Sync even EEXIST: a previous mkdir may have been interrupted before its
	// parent's publication reached disk.
	if syncErr := sync(fd); syncErr != nil {
		if errors.Is(err, unix.EEXIST) && (errors.Is(syncErr, syscall.EINVAL) || errors.Is(syncErr, syscall.ENOTSUP) || errors.Is(syncErr, syscall.EROFS)) {
			return err
		}
		return syncErr
	}
	return err
}

func (s Store) sync(dir *os.File) error {
	if s.syncDirectory != nil {
		return s.syncDirectory(dir)
	}
	return filewrite.SyncDir(dir, nil)
}

func privateDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("message directory must be a clean absolute path")
	}
	file, err := worktreesecure.OpenAbsoluteDirectoryNoFollowWith(privateOpener{}, path, true)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0700 {
		_ = file.Close()
		return nil, fmt.Errorf("message directory must be private mode 0700: %s", path)
	}
	return file, nil
}

func (s Store) directory(agent string) (*os.File, error) {
	if err := ValidateAgent(agent); err != nil {
		return nil, err
	}
	root, err := privateDirectory(s.Root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	fd, err := worktreesecure.OpenOrCreateNoFollowDirectoryWith(privateOpener{}, int(root.Fd()), agent)
	if err != nil {
		return nil, err
	}
	child := os.NewFile(uintptr(fd), "wb-message-inbox")
	info, err := child.Stat()
	if err != nil || info.Mode().Perm() != 0700 {
		_ = child.Close()
		return nil, fmt.Errorf("agent inbox must be private mode 0700")
	}
	return child, nil
}

func (s Store) Deliver(ctx context.Context, m Message) (Receipt, error) {
	if err := m.Validate(); err != nil {
		return Receipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	dir, err := s.directory(m.To)
	if err != nil {
		return Receipt{}, err
	}
	defer func() { _ = dir.Close() }()
	if err = filewrite.WriteJSONImmutableAt(dir, m.ID+".json", m, true, nil); err != nil {
		return Receipt{}, err
	}
	if err = s.sync(dir); err != nil {
		return Receipt{}, err
	}
	result := Receipt{ID: m.ID, Digest: m.Digest(), Status: "durable", Wake: "not-bound"}
	notifyWaiter(filepath.Join(s.Root, m.To, "wait.sock"))
	ack, ackErr := filewrite.ReadAt(dir, m.ID+".ack")
	if ackErr == nil {
		if string(ack) != m.Digest() {
			return result, fmt.Errorf("acknowledgment digest does not match message")
		}
		result.Wake = "acknowledged"
		return result, nil
	}
	if !errors.Is(ackErr, os.ErrNotExist) {
		return result, ackErr
	}
	if s.Notify != nil {
		result.Wake, err = s.Notify(ctx, m)
	}
	return result, err
}

func readMessage(dir *os.File, name string) (Message, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Message{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer func() { _ = file.Close() }()
	return decodeFile(file)
}

func decodeFile(file *os.File) (Message, error) {
	info, err := file.Stat()
	if err != nil {
		return Message{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxEnvelopeBytes {
		return Message{}, fmt.Errorf("message entry must be a bounded regular file")
	}
	return Decode(file)
}

func (s Store) pending(agent string) ([]Message, error) {
	return s.pendingLimit(agent, 100)
}

func (s Store) pendingLimit(agent string, limit int) ([]Message, error) {
	dir, err := s.directory(agent)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	return pendingIn(dir, agent, limit)
}

func pendingIn(dir *os.File, agent string, limit int) ([]Message, error) {
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	messages := make([]Message, 0)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if err = ValidateID(id); err != nil {
			return nil, err
		}
		message, readErr := readMessage(dir, entry.Name())
		if readErr != nil {
			return nil, readErr
		}
		if message.ID != id || message.To != agent {
			return nil, fmt.Errorf("message identity does not match inbox")
		}
		ack, ackErr := filewrite.ReadAt(dir, id+".ack")
		if ackErr == nil {
			if string(ack) != message.Digest() {
				return nil, fmt.Errorf("acknowledgment digest does not match message")
			}
			continue
		}
		if !errors.Is(ackErr, os.ErrNotExist) {
			return nil, ackErr
		}
		messages = append(messages, message)
		if limit > 0 && len(messages) == limit {
			break
		}
	}
	return messages, nil
}

// Ack records consumption without deleting the immutable message. Repeating a
// delivery with the same ID and content can never resurrect an acknowledged ID.
func (s Store) Ack(agent, id string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	dir, err := s.directory(agent)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	message, err := readMessage(dir, id+".json")
	if err != nil {
		return err
	}
	if message.ID != id || message.To != agent {
		return fmt.Errorf("message identity does not match inbox")
	}
	if err = filewrite.WriteBytesImmutableAt(dir, id+".ack", []byte(message.Digest()), 0600, true, nil); err != nil {
		return err
	}
	return s.sync(dir)
}
