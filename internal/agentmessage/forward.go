package agentmessage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// Forward transfers a local recipient inbox through an operator-configured SSH
// endpoint. Both envelope identities must match the fixed route. Local ACK means
// confirmed remote durability, not remote agent processing. Rejected/ambiguous
// wake outcomes are terminal; preflight failures remain queued for wake retry.
//
// Once performs one directory pass in pages of at most 100 entries. Memory is
// bounded, but I/O is proportional to retained inbox files. A failed entry does
// not restart the cursor or prevent later entries from being considered. A
// continuous forwarder listens before scanning and wakes on local delivery;
// failed passes retry after two seconds. Restarts replay the same immutable IDs.
func (s SSH) Forward(ctx context.Context, store Store, from, to string, once bool) error {
	if err := s.Config.Validate(); err != nil {
		return err
	}
	if err := ValidateAgent(from); err != nil {
		return err
	}
	if err := ValidateAgent(to); err != nil {
		return err
	}
	if from == to {
		return fmt.Errorf("forward sender and recipient must differ")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if once {
		err := s.forwardPass(ctx, store, from, to)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	path := filepath.Join(store.Root, to, "wait.sock")
	if len(path) > 103 {
		return fmt.Errorf("forward wait socket exceeds portable path limit; shorten the projects root or agent name")
	}
	dir, err := store.directory(to)
	if err != nil {
		return fmt.Errorf("forward local inbox could not be opened")
	}
	_ = dir.Close()
	conn, release, err := store.openWaitSocket(to, path)
	if err != nil {
		return fmt.Errorf("forward could not acquire inbox waiter; check for another receiver or forwarder")
	}
	defer release()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	return s.forwardListen(ctx, store, from, to, conn)
}

func (s SSH) forwardListen(ctx context.Context, store Store, from, to string, conn *net.UnixConn) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := s.forwardPass(ctx, store, from, to)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			var signal [1]byte
			if _, _, readErr := conn.ReadFromUnix(signal[:]); readErr != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("forward inbox wake listener failed")
			}
			continue
		}
		if s.ReportRetry != nil {
			s.ReportRetry(err)
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s SSH) forwardPass(ctx context.Context, store Store, from, to string) error {
	dir, err := store.directory(to)
	if err != nil {
		return fmt.Errorf("forward local inbox could not be opened")
	}
	defer func() { _ = dir.Close() }()
	return s.forwardPages(ctx, store, from, to, dir, dir.ReadDir)
}

func (s SSH) forwardPages(ctx context.Context, store Store, from, to string, dir *os.File, readPage func(int) ([]os.DirEntry, error)) error {
	var firstError error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := readPage(100)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("forward local inbox could not be read")
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := s.forwardEntry(ctx, store, from, to, dir, entry.Name()); err != nil && firstError == nil {
				firstError = err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return firstError
		}
	}
}

func (s SSH) forwardEntry(ctx context.Context, store Store, from, to string, dir *os.File, name string) error {
	if !strings.HasSuffix(name, ".json") {
		return nil
	}
	id := strings.TrimSuffix(name, ".json")
	if ValidateID(id) != nil {
		return fmt.Errorf("forward local envelope filename is invalid")
	}
	m, err := readMessage(dir, name)
	if err != nil {
		return fmt.Errorf("forward local envelope could not be read or validated")
	}
	if m.ID != id || m.To != to || m.From != from {
		return fmt.Errorf("forward local envelope does not match the configured route or identity; left queued")
	}
	ack, ackErr := filewrite.ReadAt(dir, id+".ack")
	if ackErr == nil {
		if string(ack) != m.Digest() {
			return fmt.Errorf("forward local acknowledgment digest does not match")
		}
		return nil
	}
	if !errors.Is(ackErr, os.ErrNotExist) {
		return fmt.Errorf("forward local acknowledgment could not be read")
	}
	// Send validates receipt ID, digest and durable status before returning it.
	receipt, sendErr := s.Send(ctx, m)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if receipt.Status != "durable" {
		return sendErr
	}
	if receipt.Wake == "preflight-failed" {
		return fmt.Errorf("forward remote message is durable but wake preflight failed; left queued for retry")
	}
	if sendErr != nil && receipt.Wake != "ambiguous" && receipt.Wake != "rejected" {
		return sendErr
	}
	if store.Ack(to, id) != nil {
		return fmt.Errorf("forward remote delivery is durable but local acknowledgment failed; safe to replay")
	}
	return nil
}
