package session

import (
	"context"
	"os"
	"time"
)

// Cancellable acquisition uses the same persistent lock inode as ordinary
// registry writers. No blocked goroutine or descriptor-close race survives a
// canceled waiter. Native nonblocking locks never enqueue an asynchronous IO.
func lockRegistryFileContext(ctx context.Context, file *os.File) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		unlock, acquired, err := tryLockRegistryFile(file)
		if err != nil {
			return nil, err
		}
		if acquired {
			if err := ctx.Err(); err != nil {
				unlock()
				return nil, err
			}
			return unlock, nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
