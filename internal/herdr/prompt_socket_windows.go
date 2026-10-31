//go:build windows

package herdr

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/Microsoft/go-winio"
)

// Herdr0.9.1 uses interprocess2.4.2 GenericNamespaced, which prepends this
// literal local pipe namespace to the original recorded path unchanged.
// It does not hash, slash-normalize, or special-case an existing pipe prefix.
func herdrPipeName(recorded string) (string, error) {
	if recorded == "" || strings.ContainsRune(recorded, 0) {
		return "", fmt.Errorf("invalid recorded Herdr pipe name")
	}
	return `\\.\pipe\` + recorded, nil
}
func dialHerdrSocket(ctx context.Context, recorded string) (net.Conn, error) {
	name, err := herdrPipeName(recorded)
	if err != nil {
		return nil, err
	}
	return winio.DialPipeContext(ctx, name)
}
