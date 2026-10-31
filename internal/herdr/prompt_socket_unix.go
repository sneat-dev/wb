//go:build !windows

package herdr

import (
	"context"
	"fmt"
	"net"
	"os"
)

func dialHerdrSocket(ctx context.Context, path string) (net.Conn, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSocket == 0 || before.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("Herdr endpoint is not one private socket")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || after.Mode()&os.ModeSocket == 0 || after.Mode().Perm() != 0o600 {
		_ = conn.Close()
		return nil, fmt.Errorf("Herdr endpoint changed during connection")
	}
	return conn, nil
}
